package ops

import "errors"

var (
	// errNilVerifier is returned when New is given no chain verifier.
	errNilVerifier = errors.New("ops: scheduler requires a non-nil ChainVerifier")
	// errNilLister is returned when New is given no chain lister.
	errNilLister = errors.New("ops: scheduler requires a non-nil ChainLister")
)
