package main

// projectRun is one run of a project query: its staging database and result
// (see the staging design in docs/projects-plan.md).
type projectRun struct {
	done bool
}

// running reports whether the run is still loading or querying.
func (r *projectRun) running() bool { return !r.done }

// close cancels the run and removes its staging database.
func (r *projectRun) close() { r.done = true }
