#import <EventKit/EventKit.h>
#import <Foundation/Foundation.h>
#import <objc/message.h>
#import <objc/runtime.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

#include "assignment_bridge_darwin.h"

void rr_assignment_free(char *value) {
    if (value) free(value);
}

static rr_assignment_result_t rr_error(NSString *message) {
    rr_assignment_result_t result = {NULL, NULL};
    result.error = strdup((message ?: @"unknown assignment error").UTF8String);
    return result;
}

static rr_assignment_result_t rr_json(id object) {
    NSError *error = nil;
    NSData *data = [NSJSONSerialization dataWithJSONObject:object options:0 error:&error];
    if (!data) return rr_error(error.localizedDescription);
    NSString *string = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    rr_assignment_result_t result = {strdup(string.UTF8String), NULL};
    return result;
}

static BOOL load_reminderkit(void) {
    static BOOL loaded = NO;
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        void *reminderKit = dlopen("/System/Library/PrivateFrameworks/ReminderKit.framework/ReminderKit", RTLD_NOW | RTLD_LAZY);
        void *internal = dlopen("/System/Library/PrivateFrameworks/ReminderKitInternal.framework/ReminderKitInternal", RTLD_NOW | RTLD_LAZY);
        loaded = reminderKit != NULL && internal != NULL;
    });
    return loaded;
}

static Ivar find_ivar(Class cls, const char *name) {
    while (cls) {
        Ivar value = class_getInstanceVariable(cls, name);
        if (value) return value;
        cls = class_getSuperclass(cls);
    }
    return NULL;
}

static id rem_object_from_backing_object(id eventKitObject, const char *expectedClass) {
    if (!eventKitObject || !load_reminderkit()) return nil;
    SEL backingSelector = NSSelectorFromString(@"backingObject");
    if (![eventKitObject respondsToSelector:backingSelector]) return nil;
    id backing = ((id (*)(id, SEL))objc_msgSend)(eventKitObject, backingSelector);
    Ivar remObjectIvar = backing ? find_ivar([backing class], "_remObject") : NULL;
    if (!remObjectIvar) return nil;
    id remObject = object_getIvar(backing, remObjectIvar);
    Class expected = objc_getClass(expectedClass);
    return expected && [remObject isKindOfClass:expected] ? remObject : nil;
}

static NSString *object_id_string(id objectID) {
    if (!objectID) return @"";
    SEL urlSelector = NSSelectorFromString(@"urlRepresentation");
    if ([objectID respondsToSelector:urlSelector]) {
        id url = ((id (*)(id, SEL))objc_msgSend)(objectID, urlSelector);
        if ([url isKindOfClass:[NSURL class]]) return [url absoluteString] ?: @"";
        if ([url isKindOfClass:[NSString class]]) return url;
    }
    return [objectID description] ?: @"";
}

static NSArray *sharees_for_list(EKCalendar *calendar) {
    id remList = rem_object_from_backing_object(calendar, "REMList");
    if (!remList) return @[];
    SEL contextSelector = NSSelectorFromString(@"shareeContext");
    if (![remList respondsToSelector:contextSelector]) return @[];
    id context = ((id (*)(id, SEL))objc_msgSend)(remList, contextSelector);
    if (!context) return @[];

    NSMutableArray *sharees = [NSMutableArray array];
    SEL shareesSelector = NSSelectorFromString(@"sharees");
    if ([context respondsToSelector:shareesSelector]) {
        id values = ((id (*)(id, SEL))objc_msgSend)(context, shareesSelector);
        if ([values isKindOfClass:[NSArray class]]) [sharees addObjectsFromArray:values];
        if ([values isKindOfClass:[NSSet class]]) [sharees addObjectsFromArray:[values allObjects]];
    }
    SEL ownerSelector = NSSelectorFromString(@"sharedOwner");
    if ([context respondsToSelector:ownerSelector]) {
        id owner = ((id (*)(id, SEL))objc_msgSend)(context, ownerSelector);
        if (owner && ![sharees containsObject:owner]) [sharees addObject:owner];
    }
    return sharees;
}

static id value(id object, NSString *key) {
    if (!object) return nil;
    @try {
        return [object valueForKey:key];
    } @catch (__unused NSException *exception) {
        return nil;
    }
}

static id sharee_matching_id(NSArray *sharees, id assigneeID) {
    for (id sharee in sharees) {
        id objectID = value(sharee, @"objectID");
        if (objectID && [objectID isEqual:assigneeID]) return sharee;
    }
    return nil;
}

static NSDictionary *assignment_dictionary(EKReminder *reminder) {
    id remReminder = rem_object_from_backing_object(reminder, "REMReminder");
    if (!remReminder) return nil;
    SEL contextSelector = NSSelectorFromString(@"assignmentContext");
    if (![remReminder respondsToSelector:contextSelector]) return nil;
    id context = ((id (*)(id, SEL))objc_msgSend)(remReminder, contextSelector);
    SEL currentSelector = NSSelectorFromString(@"currentAssignment");
    if (!context || ![context respondsToSelector:currentSelector]) return nil;
    id assignment = ((id (*)(id, SEL))objc_msgSend)(context, currentSelector);
    if (!assignment) return nil;

    id assigneeID = value(assignment, @"assigneeID");
    id sharee = sharee_matching_id(sharees_for_list(reminder.calendar), assigneeID);
    NSString *name = value(sharee, @"displayName") ?: value(sharee, @"formattedName") ?: @"";
    NSString *address = value(sharee, @"address") ?: @"";
    return @{
        @"id": object_id_string(assigneeID),
        @"name": name,
        @"address": address,
    };
}

static EKReminder *find_reminder(NSString *identifier) {
    static EKEventStore *store = nil;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ store = [[EKEventStore alloc] init]; });
    id item = [store calendarItemWithIdentifier:identifier];
    return [item isKindOfClass:[EKReminder class]] ? item : nil;
}

rr_assignment_result_t rr_assignment_get(const char *reminder_id) {
    @autoreleasepool {
        if (!reminder_id) return rr_error(@"reminder ID is required");
        EKReminder *reminder = find_reminder([NSString stringWithUTF8String:reminder_id]);
        if (!reminder) return rr_error(@"reminder not found");
        NSDictionary *assignment = assignment_dictionary(reminder);
        return rr_json(assignment ?: @{});
    }
}

static BOOL string_matches(NSString *left, NSString *right) {
    return left.length > 0 && right.length > 0 && [left caseInsensitiveCompare:right] == NSOrderedSame;
}

static id find_target_sharee(NSArray *sharees, NSDictionary *requested) {
    NSString *requestedID = requested[@"id"] ?: @"";
    NSString *requestedAddress = requested[@"address"] ?: @"";
    NSString *requestedName = requested[@"name"] ?: @"";
    for (id sharee in sharees) {
        if (string_matches(object_id_string(value(sharee, @"objectID")), requestedID) ||
            string_matches(value(sharee, @"address"), requestedAddress) ||
            string_matches(value(sharee, @"displayName"), requestedName) ||
            string_matches(value(sharee, @"formattedName"), requestedName)) {
            return sharee;
        }
    }
    return nil;
}

static id reminder_store(id remReminder) {
    Ivar storeIvar = find_ivar([remReminder class], "_store");
    id store = storeIvar ? object_getIvar(remReminder, storeIvar) : nil;
    SEL selector = NSSelectorFromString(@"store");
    if (!store && [remReminder respondsToSelector:selector]) {
        store = ((id (*)(id, SEL))objc_msgSend)(remReminder, selector);
    }
    return store;
}

rr_assignment_result_t rr_assignment_set(const char *reminder_id, const char *assignment_json) {
    @autoreleasepool {
        if (!reminder_id || !assignment_json) return rr_error(@"reminder ID and assignment JSON are required");
        EKReminder *reminder = find_reminder([NSString stringWithUTF8String:reminder_id]);
        if (!reminder) return rr_error(@"reminder not found");
        id remReminder = rem_object_from_backing_object(reminder, "REMReminder");
        if (!remReminder) return rr_error(@"ReminderKit reminder is unavailable");

        NSData *data = [NSData dataWithBytes:assignment_json length:strlen(assignment_json)];
        NSError *parseError = nil;
        id requested = [NSJSONSerialization JSONObjectWithData:data options:0 error:&parseError];
        if (!requested || ![requested isKindOfClass:[NSDictionary class]]) {
            return rr_error(parseError.localizedDescription ?: @"invalid assignment JSON");
        }

        NSArray *sharees = sharees_for_list(reminder.calendar);
        BOOL clear = [requested count] == 0;
        id target = clear ? nil : find_target_sharee(sharees, requested);
        if (!clear && !target) return rr_error(@"assignee is not a participant in this shared iCloud list");

        id store = reminder_store(remReminder);
        Class requestClass = objc_getClass("REMSaveRequest");
        if (!store || !requestClass) return rr_error(@"ReminderKit save API is unavailable");
        id request = [requestClass alloc];
        SEL initSelector = NSSelectorFromString(@"initWithStore:");
        request = [request respondsToSelector:initSelector]
            ? ((id (*)(id, SEL, id))objc_msgSend)(request, initSelector, store)
            : nil;
        SEL updateSelector = NSSelectorFromString(@"updateReminder:");
        id change = request && [request respondsToSelector:updateSelector]
            ? ((id (*)(id, SEL, id))objc_msgSend)(request, updateSelector, remReminder)
            : nil;
        SEL assignmentContextSelector = NSSelectorFromString(@"assignmentContext");
        id context = change && [change respondsToSelector:assignmentContextSelector]
            ? ((id (*)(id, SEL))objc_msgSend)(change, assignmentContextSelector)
            : nil;
        SEL removeSelector = NSSelectorFromString(@"removeAllAssignments");
        if (!context || ![context respondsToSelector:removeSelector]) {
            return rr_error(@"ReminderKit assignment API is unavailable");
        }

        id oldContext = value(remReminder, @"assignmentContext");
        id current = value(oldContext, @"currentAssignment");
        id originatorID = value(current, @"originatorID");
        NSInteger status = current ? [value(current, @"status") integerValue] : 0;
        ((void (*)(id, SEL))objc_msgSend)(context, removeSelector);

        if (target) {
            id targetID = value(target, @"objectID");
            if (!originatorID) {
                id remList = rem_object_from_backing_object(reminder.calendar, "REMList");
                id shareeContext = value(remList, @"shareeContext");
                originatorID = value(value(shareeContext, @"sharedOwner"), @"objectID") ?: targetID;
            }
            SEL addSelector = NSSelectorFromString(@"addAssignmentWithAssigneeID:originatorID:status:");
            if (![context respondsToSelector:addSelector]) return rr_error(@"ReminderKit cannot add assignments");
            ((id (*)(id, SEL, id, id, NSInteger))objc_msgSend)(context, addSelector, targetID, originatorID, status);
        }

        SEL saveSelector = NSSelectorFromString(@"saveSynchronouslyWithError:");
        NSError *saveError = nil;
        BOOL saved = request && [request respondsToSelector:saveSelector]
            ? ((BOOL (*)(id, SEL, NSError **))objc_msgSend)(request, saveSelector, &saveError)
            : NO;
        if (!saved) return rr_error(saveError.localizedDescription ?: @"failed to save assignment");
        return rr_json(assignment_dictionary(find_reminder([NSString stringWithUTF8String:reminder_id])) ?: @{});
    }
}
