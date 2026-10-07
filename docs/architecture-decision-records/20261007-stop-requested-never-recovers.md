# A stopped pipeline is never recovered, and a stop never drops the error

## Summary

Once a stop has been requested for a pipeline run, by a user `Stop` (graceful or forced) or by
`StopAll` during shutdown, that run is never sent into error recovery and never restarted. If the
run ends with an error, it is reported as **stopped with the error recorded**, not as degraded:
`UserStopped` for a user stop, `SystemStopped` for a shutdown. The error is kept as the pipeline's
error message, returned by `WaitPipeline` and logged. It is not treated as a failure, so OnFailure
handlers do not run and `pipelines.exit-on-degraded` does not trip. Once shutdown has begun, no
pipeline run is started, and `Wait` does not return while any run is live.

Both engines (`pkg/lifecycle` and `pkg/lifecycle-poc`) follow this. Decided by DeVaris, 2026-10-07,
for v0.20.0 ([#2901](https://github.com/ConduitIO/conduit/issues/2901)).

## Context

The cleanup goroutine of a run classified the run by its tomb error alone: no error meant
stopped, a fatal error meant degraded, any other error meant recovery. A stop request played no
part. That produced two bugs in the default engine.

- A user `Stop` followed by a transient error during the drain (for example a destination write
  failing for a batch already in flight) went into recovery, and the pipeline the user had just
  stopped was restarted after the backoff.
- On a non-graceful runtime shutdown the runtime calls `StopAll(err)`. Every source returns `err`
  as its own error, so every pipeline went into recovery and was restarted inside the 30-second
  exit timeout. `Wait` returned once the pre-restart runs were dead, and the runtime then flushed
  the persister and closed the database with the restarted runs still live (invariant 7). When
  the cause was fatal, for example exit-on-degraded after another pipeline's fatal error, every
  other pipeline was marked `Degraded` instead, and was not started again on the next boot.

arch-v2 already kept a stopped run out of recovery, but reported it stopped and dropped the error.

Two outcomes were considered for "a stop was requested and the run ended with an error":

- **Degraded with the error.** The error stays visible and OnFailure runs. But a transient error
  during a SIGTERM drain, or any pipeline caught in a non-graceful shutdown, would then need a
  manual start after the restart, because the next boot only starts `SystemStopped` pipelines.
  A user stop whose drain errors would also shut Conduit down under exit-on-degraded.
- **Stopped, error dropped** (arch-v2 before this decision). The status is right, but the error
  that interrupted the drain is lost, and with it the only signal that in-flight records were
  nacked and will be redelivered.

Neither is acceptable on its own. The status should describe what the operator or the system did
(it stopped the pipeline). The error should describe what happened during the drain.

## Decision

1. A stop request is recorded on the run before any node or worker is told to stop: by `Stop`
   (graceful and forced) and by `StopAll` (graceful and forced). It is per run, so a restarted run
   starts without one, and only the first request counts.
2. A run whose stop was requested never enters recovery. A recovery already waiting out its
   backoff is abandoned when a stop is requested or shutdown begins.
3. Its terminal status follows who stopped it:

   | Case | Status | Error |
   | --- | --- | --- |
   | User `Stop` (graceful), drain ends cleanly | `UserStopped` | none |
   | User `Stop` (graceful), error during the drain | `UserStopped` | recorded |
   | User `Stop` (force) | `UserStopped` | `force stop` recorded |
   | User `Stop` while the run waits out a recovery backoff | `UserStopped` | the error the run failed with, recorded |
   | `StopAll`, drain ends cleanly | `SystemStopped` | none |
   | `StopAll`, error during the drain | `SystemStopped` | recorded |
   | `StopAll` with a non-graceful reason (runtime error, exit-on-degraded) | `SystemStopped` | the reason, recorded |
   | `StopAll` while the run waits out a recovery backoff | `SystemStopped` | the error the run failed with, recorded |
   | Fatal error the run hit on its own, before any stop | `Degraded` | recorded; OnFailure runs |
   | Transient error, no stop requested | `Recovering`, then restarted | as today |

   "Recorded" means: the error is the pipeline's error message (visible in the API, `inspect` and
   the UI), the terminal error that `WaitPipeline` returns, and a warning in the log. A stopped run
   does not notify OnFailure handlers, so exit-on-degraded does not fire for it.
4. A fatal error that was already on the run when the stop was requested still degrades it. This
   keeps the pipeline whose own failure triggered exit-on-degraded `Degraded`, while the pipelines
   that shutdown then stops are `SystemStopped`.
5. `StopAll` puts the service into shutdown mode for good. `Start` and recovery restarts are
   refused with `pipeline.shutting_down`. `StopAll` stops every run that is still alive, whatever
   status its pipeline currently shows: a run is published before it announces `Running`, so for
   a moment it carries the previous run's status. A run that was already starting when `StopAll`
   ran stops itself as soon as it is published, and `Wait` waits on a count of live runs, not a
   snapshot of the running pipelines.

## Consequences

- A user-stopped pipeline is never restarted behind the user's back, and no pipeline is started
  during shutdown, so the runtime never closes the database under a live run.
- After any shutdown, graceful or not, pipelines that were stopped by it are `SystemStopped` and
  start again on the next boot, resuming from their last persisted position (at-least-once replay
  of anything unacked). This changes the previous behaviour for a fatal shutdown cause: other
  pipelines used to be left `Degraded` and needed a manual start.
- A forced stop is now reported `UserStopped` with `force stop` recorded, where it used to be
  `Degraded`. It no longer fires OnFailure, so it no longer shuts Conduit down under
  exit-on-degraded.
- A drain error no longer disappears in arch-v2: it is recorded as in the default engine.
- Clients that read `Degraded` as "something went wrong" must also look at the error message of a
  stopped pipeline. A non-empty error on a stopped pipeline means the drain did not end cleanly.
- `conduit pipelines start` or API `Start` racing a shutdown fails with `pipeline.shutting_down`
  (gRPC `Unavailable`).

## Related

- [#2901](https://github.com/ConduitIO/conduit/issues/2901): the bug report and the decision.
- [#2896](https://github.com/ConduitIO/conduit/issues/2896) / #2904: the tomb race that made the
  restart in #2901 deterministic.
- [20240812-recover-from-pipeline-errors](../design-documents/20240812-recover-from-pipeline-errors.md):
  the recovery mechanism this narrows.
- [20260704-graceful-shutdown-sigterm](../design-documents/20260704-graceful-shutdown-sigterm.md):
  the shutdown path whose invariant-7 guarantee this restores.
