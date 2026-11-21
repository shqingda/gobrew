package engine

import (
	"errors"
	"testing"

	"github.com/shqingda/gobrew/internal/model"
)

// A successful operation keeps exit status zero when releasing its cache
// leases fails afterwards; a failed one reports both errors.
func TestCloseFailureAfterSuccessIsAWarning(t *testing.T) {
	t.Parallel()
	ok := model.Result{Succeeded: []string{"jq"}}
	result, err := closeOperation(ok, nil, errors.New("cache cleanup: permission denied"))
	if err != nil || len(result.Warnings) != 1 || len(result.Succeeded) != 1 {
		t.Fatalf("success became %v, %+v", err, result)
	}
	applyErr := errors.New("apply failed")
	_, err = closeOperation(model.Result{}, applyErr, errors.New("unlock"))
	if !errors.Is(err, applyErr) || err.Error() == applyErr.Error() {
		t.Fatalf("failure lost an error: %v", err)
	}
	if result, err := closeOperation(ok, nil, nil); err != nil || result.Warnings != nil {
		t.Fatalf("clean close changed the result: %v %+v", err, result)
	}
}
