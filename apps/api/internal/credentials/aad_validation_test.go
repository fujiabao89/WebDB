package credentials

import (
	"math"
	"testing"

	"github.com/google/uuid"
)

type aadValidationCase struct {
	name          string
	secretVersion int
	suite         string
	kekVersion    int
	wantCode      ErrorCode
}

func TestBuildAADRejectsInvalidSecurityBindingInputs(t *testing.T) {
	t.Parallel()

	ws := uuid.New()
	ref := uuid.New()

	tests := []aadValidationCase{
		{
			name:          "unknown suite",
			secretVersion: 1,
			suite:         "UNKNOWN",
			kekVersion:    1,
			wantCode:      ErrUnknownSuite,
		},
		{
			name:          "zero secret version",
			secretVersion: 0,
			suite:         SuiteAES256GCMv1,
			kekVersion:    1,
			wantCode:      ErrInternalError,
		},
		{
			name:          "negative secret version",
			secretVersion: -1,
			suite:         SuiteAES256GCMv1,
			kekVersion:    1,
			wantCode:      ErrInternalError,
		},
		{
			name:          "zero kek version",
			secretVersion: 1,
			suite:         SuiteAES256GCMv1,
			kekVersion:    0,
			wantCode:      ErrInternalError,
		},
		{
			name:          "negative kek version",
			secretVersion: 1,
			suite:         SuiteAES256GCMv1,
			kekVersion:    -1,
			wantCode:      ErrInternalError,
		},
	}

	if uint64(^uint(0)) > math.MaxUint32 {
		tests = append(tests,
			aadValidationCase{
				name:          "secret version exceeds uint32",
				secretVersion: int(uint64(math.MaxUint32) + 1),
				suite:         SuiteAES256GCMv1,
				kekVersion:    1,
				wantCode:      ErrInternalError,
			},
			aadValidationCase{
				name:          "kek version exceeds uint32",
				secretVersion: 1,
				suite:         SuiteAES256GCMv1,
				kekVersion:    int(uint64(math.MaxUint32) + 1),
				wantCode:      ErrInternalError,
			},
		)
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			aad, err := BuildAAD(
				ws,
				ref,
				tt.secretVersion,
				tt.suite,
				tt.kekVersion,
			)
			if err == nil {
				t.Fatalf("BuildAAD() error = nil, aad = %x", aad)
			}
			if !IsErrorCode(err, tt.wantCode) {
				t.Fatalf("BuildAAD() error = %v, want code %s", err, tt.wantCode)
			}
			if aad != nil {
				t.Fatalf("BuildAAD() aad = %x, want nil on validation failure", aad)
			}
		})
	}
}
