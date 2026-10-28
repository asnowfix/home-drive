package syncer

import (
	"context"
	"errors"
	"fmt"
)

// execRemove deletes the remote object for the root-relative path rel.
// An already-absent object (ErrRemoteNotFound) is the desired end state, so
// it counts as success: returning nil stops retryFunc from retrying five
// times with backoff and keeps recordResult from emitting push.failure
// (issue #107). Typical cause: the pull side already removed the object.
func (s *Syncer) execRemove(ctx context.Context, rel string) error {
	err := s.remote.DeleteFile(ctx, rel)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRemoteNotFound) {
		s.logger.Debug("remote object already absent, delete is a no-op", "path", rel)
		return nil
	}
	return fmt.Errorf("delete file %q: %w", rel, err)
}
