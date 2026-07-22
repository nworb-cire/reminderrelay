//go:build !darwin || !cgo

package reminders

import (
	"fmt"

	"github.com/njoerd114/reminderrelay/internal/model"
)

func readAssignment(string) (*model.Assignment, error) {
	return nil, nil
}

func writeAssignment(string, *model.Assignment) (*model.Assignment, error) {
	return nil, fmt.Errorf("reminder assignments require macOS with cgo")
}
