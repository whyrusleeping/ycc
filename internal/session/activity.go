package session

import "github.com/whyrusleeping/ycc/internal/jobs"

// JobInfos snapshots every job in the session's registry (all owners), for the
// activity graph. Nil when the session has no job registry.
func (s *Session) JobInfos() []jobs.Info {
	if s.deps == nil || s.deps.Jobs == nil {
		return nil
	}
	return s.deps.Jobs.List("", true)
}

// JobOutput reads a job's retained output by absolute byte cursor (or retained
// tail), without affecting notification state.
func (s *Session) JobOutput(id string, offset int64, limit, tailLines int) (jobs.Output, jobs.Info, bool) {
	if s.deps == nil || s.deps.Jobs == nil {
		return jobs.Output{}, jobs.Info{}, false
	}
	job, ok := s.deps.Jobs.Get(id)
	if !ok {
		return jobs.Output{}, jobs.Info{}, false
	}
	return job.Output(offset, limit, tailLines), job.Info(), true
}
