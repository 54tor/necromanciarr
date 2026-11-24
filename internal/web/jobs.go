package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"necromanciarr/internal/i18n"
)

// maxJobHistory borne l'historique en mémoire : les jobs ne sont pas persistés, le résultat durable est en base.
const maxJobHistory = 50

// JobState est l'état d'un job : running, puis done ou failed.
type JobState string

// États d'un job.
const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
)

// Job suit une action longue lancée par l'API : Result est le DTO de l'action une fois finie, Error son erreur au
// format stable.
type Job struct {
	ID         int64      `json:"id"`
	Action     string     `json:"action"`
	State      JobState   `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Result     any        `json:"result,omitempty"`
	Error      *ErrorBody `json:"error,omitempty"`
}

// jobRunner exécute un job à la fois dans le contexte du serveur (jamais celui de la requête, qui finit avec la
// réponse) : un arrêt du serveur annule le job en cours.
type jobRunner struct {
	describe func(error) (int, ErrorBody)
	logLine  func(level slog.Level, code string, key i18n.Key, params map[string]string)
	ctx      context.Context
	mutex    sync.Mutex
	history  []*Job
	running  bool
	closed   bool
	lastID   int64
	group    sync.WaitGroup
}

func newJobRunner(ctx context.Context, describe func(error) (int, ErrorBody), logLine func(slog.Level, string, i18n.Key, map[string]string)) *jobRunner {
	return &jobRunner{ctx: ctx, describe: describe, logLine: logLine}
}

// errJobRunning : un job tourne déjà ; même code que le verrou d'étape du Service.
var errJobRunning = newAPIError(http.StatusConflict, errorCodeStageRunning, i18n.Message{Key: i18n.KeyErrorWebJobRunning})

// errShuttingDown : le serveur s'arrête, aucun job ne démarre plus (WaitGroup.Go après Wait serait une course).
var errShuttingDown = newAPIError(http.StatusServiceUnavailable, errorCodeShuttingDown, i18n.Message{Key: i18n.KeyErrorWebShuttingDown})

// startJob lance work en arrière-plan et répond 202 avec le job ; le résultat et l'erreur sont masqués à la lecture
// (writeJSON), comme toute réponse.
func (handler *Handler) startJob(action string, work func(ctx context.Context) (any, error)) (any, int, error) {
	job, err := handler.jobs.start(action, work)
	if err != nil {
		return nil, 0, err
	}
	return job, http.StatusAccepted, nil
}

func (runner *jobRunner) start(action string, work func(ctx context.Context) (any, error)) (Job, error) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	if runner.closed || runner.ctx.Err() != nil {
		return Job{}, errShuttingDown
	}
	if runner.running {
		return Job{}, errJobRunning
	}
	runner.lastID++
	job := &Job{ID: runner.lastID, Action: action, State: JobRunning, StartedAt: time.Now().UTC()}
	runner.history = append(runner.history, job)
	if len(runner.history) > maxJobHistory {
		runner.history = slices.Delete(runner.history, 0, len(runner.history)-maxJobHistory)
	}
	runner.running = true
	runner.group.Go(func() { runner.finish(job, work) })
	return *job, nil
}

func (runner *jobRunner) finish(job *Job, work func(ctx context.Context) (any, error)) {
	result, err := runner.runRecovering(job, work)
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	finishedAt := time.Now().UTC()
	job.FinishedAt, job.State, job.Result = &finishedAt, JobDone, result
	if err != nil {
		status, body := runner.describe(err)
		if status == http.StatusInternalServerError {
			runner.logLine(slog.LevelError, LogCodeJobFailed, i18n.KeyLogJobFailed, jobParams(job, err.Error()))
		}
		job.State, job.Result, job.Error = JobFailed, nil, &body
	}
	runner.running = false
}

// errJobPanicked : le job a paniqué ; le serveur reste debout et le job échoue sans rien révéler.
var errJobPanicked = errors.New("job panicked")

// runRecovering exécute work et change une panique en échec du job ; la valeur de la panique n'est écrite que
// masquée, dans le journal du serveur.
func (runner *jobRunner) runRecovering(job *Job, work func(ctx context.Context) (any, error)) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			runner.logLine(slog.LevelError, LogCodeJobPanicked, i18n.KeyLogJobPanicked, jobParams(job, fmt.Sprint(recovered)))
			result, err = nil, errJobPanicked
		}
	}()
	return work(runner.ctx)
}

// list renvoie une copie de l'historique, le plus récent d'abord.
func (runner *jobRunner) list() []Job {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	jobs := make([]Job, 0, len(runner.history))
	for index := len(runner.history) - 1; index >= 0; index-- {
		jobs = append(jobs, *runner.history[index])
	}
	return jobs
}

func (runner *jobRunner) get(jobID int64) (Job, bool) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	for _, job := range runner.history {
		if job.ID == jobID {
			return *job, true
		}
	}
	return Job{}, false
}

// wait ferme le lanceur (sous le verrou, avant d'attendre) puis attend le job en cours.
func (runner *jobRunner) wait() {
	runner.mutex.Lock()
	runner.closed = true
	runner.mutex.Unlock()
	runner.group.Wait()
}
