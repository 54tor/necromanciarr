package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/pipeline"
)

func decodeJob(t *testing.T, body string) Job {
	t.Helper()
	var job Job
	if err := json.Unmarshal([]byte(body), &job); err != nil {
		t.Fatalf("job body is not JSON: %q", body)
	}
	return job
}

func startJob(t *testing.T, handler http.Handler, target, body string) Job {
	t.Helper()
	response := serve(handler, apiRequest(http.MethodPost, target, body))
	if response.Code != http.StatusAccepted {
		t.Fatalf("%s: gotValue %d %q, expectedValue 202", target, response.Code, readBody(t, response))
	}
	return decodeJob(t, readBody(t, response))
}

// waitJob interroge /api/jobs/{id} jusqu'à la fin du job.
func waitJob(t *testing.T, handler http.Handler, jobID int64) Job {
	t.Helper()
	for range pollAttempts {
		job := decodeJob(t, readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/jobs/"+strconv.FormatInt(jobID, 10), ""))))
		if job.State != JobRunning {
			return job
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("job %d still running", jobID)
	return Job{}
}

func TestJobs_oneAtATimeAndBounded(t *testing.T) {
	fake := &fakeService{stageGate: make(chan struct{})}
	handler := newTestHandler(t, fake, loopbackSettings())

	running := startJob(t, handler, "/api/stages/scan/run", `{}`)
	if running.State != JobRunning || running.Action != "run" || running.StartedAt.IsZero() {
		t.Fatalf("gotValue %+v, expectedValue a running job of the run action", running)
	}
	second := serve(handler, apiRequest(http.MethodPost, "/api/dedupe/plan", `{}`))
	expectError(t, "second job", readBody(t, second), second.Code, http.StatusConflict, app.ErrorCodeStageRunning)
	close(fake.stageGate)
	done := waitJob(t, handler, running.ID)
	if done.State != JobDone || done.FinishedAt == nil || done.Result == nil {
		t.Fatalf("gotValue %+v, expectedValue a done job with its result", done)
	}

	for range maxJobHistory + 5 {
		quick := startJob(t, handler, "/api/dedupe/plan", `{}`)
		waitJob(t, handler, quick.ID)
	}
	var history []Job
	if err := json.Unmarshal([]byte(readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/jobs", "")))), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != maxJobHistory || history[0].ID <= history[len(history)-1].ID {
		t.Fatalf("gotValue %d jobs (first %d), expectedValue %d jobs newest first", len(history), history[0].ID, maxJobHistory)
	}
	evicted := serve(handler, apiRequest(http.MethodGet, "/api/jobs/"+strconv.FormatInt(running.ID, 10), ""))
	expectError(t, "evicted job", readBody(t, evicted), evicted.Code, http.StatusNotFound, app.ErrorCodeNotFound)
}

func TestJobs_failedJobCarriesTheStableErrorCode(t *testing.T) {
	fake := &fakeService{errors: map[string]error{"RunStage": pipeline.ErrStageRunning}}
	handler := newTestHandler(t, fake, loopbackSettings())

	failed := waitJob(t, handler, startJob(t, handler, "/api/stages/verify/run", `{}`).ID)

	if failed.State != JobFailed || failed.Error == nil || failed.Error.Code != app.ErrorCodeStageRunning || failed.Result != nil {
		t.Fatalf("gotValue %+v, expectedValue a failed job with the stage_running code", failed)
	}
}

func TestJobs_serverShutdownCancelsTheRunningJob(t *testing.T) {
	fake := &fakeService{stageGate: make(chan struct{})}
	serverContext, cancel := context.WithCancel(context.Background())
	handler := NewHandlerWithAssets(serverContext, fake, &fakeHealth{}, loopbackSettings(), builtAssets())
	running := startJob(t, handler, "/api/stages/link/run", `{}`)

	cancel()
	handler.Wait()

	stopped := decodeJob(t, readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/jobs/"+strconv.FormatInt(running.ID, 10), ""))))
	if stopped.State != JobFailed {
		t.Fatalf("gotValue %+v, expectedValue the job cancelled by the server context", stopped)
	}
}

func TestJobs_maintenanceRoutesPassTheirOptions(t *testing.T) {
	fake := &fakeService{}
	handler := newTestHandler(t, fake, loopbackSettings())
	calls := []struct{ target, body, expectedCall string }{
		{"/api/dedupe/7/apply", `{"dry_run": true}`, "DedupeAccept 7 [2]"},
		{"/api/repair/scan", `{"directory": "/home/u/links"}`, "RepairScan /home/u/links"},
		{"/api/repair/9/apply", `{"accept_pending": true}`, "RepairAccept 9 [3]"},
	}
	for _, call := range calls {
		job := waitJob(t, handler, startJob(t, handler, call.target, call.body).ID)
		if job.State != JobDone || !fake.called(call.expectedCall) {
			t.Fatalf("%s: gotValue %+v, calls %v, expectedValue %q", call.target, job, fake.calls, call.expectedCall)
		}
	}
}
