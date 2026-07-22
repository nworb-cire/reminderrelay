//go:build darwin && cgo

package reminders

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework EventKit -framework AppKit
#include <stdlib.h>
#include "assignment_bridge_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/njoerd114/reminderrelay/internal/model"
)

func prepareNativeApplication() error {
	message := C.rr_prepare_application()
	if message == nil {
		return nil
	}
	defer C.rr_assignment_free(message)
	return fmt.Errorf("native application preparation: %s", C.GoString(message))
}

func assignmentResult(result C.rr_assignment_result_t) (*model.Assignment, error) {
	if result.error != nil {
		defer C.rr_assignment_free(result.error)
		return nil, fmt.Errorf("assignment bridge: %s", C.GoString(result.error))
	}
	if result.result == nil {
		return nil, nil
	}
	defer C.rr_assignment_free(result.result)
	var assignment model.Assignment
	if err := json.Unmarshal([]byte(C.GoString(result.result)), &assignment); err != nil {
		return nil, fmt.Errorf("decode assignment: %w", err)
	}
	if assignment.ID == "" && assignment.Name == "" && assignment.Address == "" {
		return nil, nil
	}
	return &assignment, nil
}

func readAssignment(uid string) (*model.Assignment, error) {
	cUID := C.CString(uid)
	defer C.free(unsafe.Pointer(cUID))
	return assignmentResult(C.rr_assignment_get(cUID))
}

func writeAssignment(uid string, assignment *model.Assignment) (*model.Assignment, error) {
	payload := []byte("{}")
	if assignment != nil {
		var err error
		payload, err = json.Marshal(assignment)
		if err != nil {
			return nil, fmt.Errorf("encode assignment: %w", err)
		}
	}
	cUID := C.CString(uid)
	cPayload := C.CString(string(payload))
	defer C.free(unsafe.Pointer(cUID))
	defer C.free(unsafe.Pointer(cPayload))
	return assignmentResult(C.rr_assignment_set(cUID, cPayload))
}
