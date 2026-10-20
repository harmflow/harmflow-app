package domain

import "time"

// ErrorClass is what an adapter observed when an attempt ended (FS-005 FR-004). Adapters classify from real
// signals only; anything they cannot tell apart is ClassTaskError or ClassUnknown, never quota.
type ErrorClass string

const (
	ClassNone               ErrorClass = ""
	ClassQuotaExhausted     ErrorClass = "quota_exhausted"
	ClassRateLimited        ErrorClass = "rate_limited"
	ClassHarnessUnavailable ErrorClass = "harness_unavailable"
	ClassNetwork            ErrorClass = "network"
	ClassTimeout            ErrorClass = "timeout"
	ClassCancelled          ErrorClass = "cancelled"
	ClassTaskError          ErrorClass = "task_error"
	ClassUnknown            ErrorClass = "unknown"
)

// AttemptEnd is the classified end of one harness process.
type AttemptEnd struct {
	Class      ErrorClass
	Message    string
	RetryAfter time.Duration // a reset time the harness reported, for rate limits
	ExitCode   int
	SessionID  string
	FinalText  string // the agent's last message, carried into the next attempt's summary
}

// Action is what the executor does after an attempt.
type Action string

const (
	ActionFinish        Action = "finish"
	ActionRetrySame     Action = "retry_same"
	ActionSwitch        Action = "switch"
	ActionNoAlternative Action = "no_alternative"
	ActionFail          Action = "fail"
)

// FailoverInput is the state the decision needs.
type FailoverInput struct {
	End              AttemptEnd
	FallbackOn       []string
	HasNextCandidate bool
	Switches         int
	MaxSwitches      int
	RateLimitRetries int
	MaxRateRetries   int
}

// DecideAfterAttempt applies D-067: quota and an unavailable harness fail over along the chain, a temporary rate
// limit retries on the same harness, and network failures, timeouts, and task errors never fail over.
func DecideAfterAttempt(in FailoverInput) Action {
	switch in.End.Class {
	case ClassNone:
		return ActionFinish
	case ClassRateLimited:
		if in.RateLimitRetries < in.MaxRateRetries {
			return ActionRetrySame
		}
		return ActionFail
	case ClassQuotaExhausted, ClassHarnessUnavailable:
		trigger := FallbackOnQuota
		if in.End.Class == ClassHarnessUnavailable {
			trigger = FallbackOnUnavailable
		}
		if !contains(in.FallbackOn, trigger) {
			return ActionFail
		}
		if !in.HasNextCandidate || in.Switches >= in.MaxSwitches {
			return ActionNoAlternative
		}
		return ActionSwitch
	default:
		return ActionFail
	}
}

// RateLimitWait returns the wait before retry n (0-based): the time the harness indicated, otherwise the initial
// wait doubling up to the maximum (D-082).
func RateLimitWait(n int, indicated, initial, max time.Duration) time.Duration {
	if indicated > 0 {
		if indicated > max {
			return max
		}
		return indicated
	}
	w := initial
	for i := 0; i < n; i++ {
		w *= 2
		if w >= max {
			return max
		}
	}
	return w
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
