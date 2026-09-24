package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

const internalWorkItemPanic = "internal panic; execution state is ambiguous; explicit resubmission required"

func (s *Server) recoverSchedulePanic(id, runID string) {
	if recover() == nil {
		return
	}
	s.logger.Printf("schedule %s recovered an internal panic:\n%s", id, debug.Stack())
	s.schedules.finish(id, runID, "failed", internalWorkItemPanic)
	s.store.AddActivity("scheduled", "Scheduled job failed", runID)
}

func (s *Server) recoverInboxPanic(path string) {
	if recover() == nil {
		return
	}
	s.logger.Printf("inbox job %s recovered an internal panic:\n%s", filepath.Base(path), debug.Stack())
	resultPath := strings.TrimSuffix(path, ".processing.json") + ".result.json"
	result, _ := json.MarshalIndent(Output{Mode: "text", Error: internalWorkItemPanic}, "", "  ")
	if err := writeInboxResult(resultPath, append(result, '\n')); err != nil {
		s.logger.Printf("panic result %s could not be stored safely: %v", filepath.Base(resultPath), err)
		return
	}
	if err := os.Remove(path); err != nil {
		s.logger.Printf("panic-marked inbox job %s could not be removed: %v", filepath.Base(path), err)
	}
}
