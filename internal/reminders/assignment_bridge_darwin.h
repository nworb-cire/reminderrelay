#ifndef REMINDERRELAY_ASSIGNMENT_BRIDGE_H
#define REMINDERRELAY_ASSIGNMENT_BRIDGE_H

typedef struct {
    char *result;
    char *error;
} rr_assignment_result_t;

rr_assignment_result_t rr_assignment_get(const char *reminder_id);
rr_assignment_result_t rr_assignment_set(const char *reminder_id, const char *assignment_json);
void rr_assignment_free(char *value);

#endif
