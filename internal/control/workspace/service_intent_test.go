package workspace

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// A request that names a long-running service is execution, not reading. The
// observed failure was routed to ASK because its head was "use"/"serve"/
// "start", so the runtime withheld the very capability the user asked for.
func TestServiceVerificationRequestsRouteToExecution(t *testing.T) {
	for _, request := range []string{
		"use npx live-server test this project",
		"serve this project and verify it",
		"start the development server and check the page",
		"use npx live-server and make sure the site works",
	} {
		t.Run(request, func(t *testing.T) {
			if got := Classify(request); got != IntentExecute {
				t.Fatalf("Classify = %s, want execute", got)
			}
			got := BuildIntentContract(request)
			if got.Kind != contract.ObjectiveExecution {
				t.Fatalf("contract kind = %s, want execution", got.Kind)
			}
		})
	}
}

// "run the app and test it" may route to FUZZ because testing is an
// investigation, but the contract still requires the process to execute: an
// answer alone is not evidence the app ran.
func TestRunTheAppAndTestItIsAnExecutionContract(t *testing.T) {
	request := "run the app and test it"
	if got := BuildIntentContract(request); got.Kind != contract.ObjectiveExecution {
		t.Fatalf("contract kind = %s, want execution", got.Kind)
	}
}

// Running the project is a stronger objective than running a command. A
// finished environment probe is not the requested execution, so the contract
// must name the live-runtime observation. A named check or a question about how
// to run the project must keep the weaker reading.
func TestRuntimeExecutionContract(t *testing.T) {
	runtime := []string{
		"run this project",
		"run this project help me",
		"run it",
		"start this project",
		"launch this project",
		"run the app",
		"start the app",
		"run the app and test it",
		"start the development server and check the page",
		"use npx live-server test this project",
		"test this project with npx live-server",
		"run npm run dev",
		"run npm start",
		"start the dev server",
	}
	for _, request := range runtime {
		t.Run(request, func(t *testing.T) {
			got := BuildIntentContract(request)
			if got.Kind != contract.ObjectiveExecution {
				t.Fatalf("kind = %s, want execution", got.Kind)
			}
			if got.ExecutionMode != contract.ExecutionRuntime {
				t.Fatalf("execution mode = %q, want runtime", got.ExecutionMode)
			}
		})
	}

	command := []string{
		"run the tests",
		"run the test suite",
		"run go test ./...",
		"run the build",
		"run npm run build",
		"run the project tests",
	}
	for _, request := range command {
		t.Run(request, func(t *testing.T) {
			got := BuildIntentContract(request)
			if got.Kind != contract.ObjectiveExecution {
				t.Fatalf("kind = %s, want execution", got.Kind)
			}
			if got.ExecutionMode == contract.ExecutionRuntime {
				t.Fatalf("execution mode = runtime for a one-shot command")
			}
		})
	}

	// Asking how to run the project is informational: the answer is the
	// deliverable, and demanding a live runtime would invent an action the user
	// did not request.
	if got := BuildIntentContract("how do I run this project"); got.Kind != contract.ObjectiveInformational {
		t.Fatalf("how-to kind = %s, want informational", got.Kind)
	}
}

// Browser testing is an investigation, not a mutation. It must reach FUZZ,
// which may execute probes but cannot change the repository.
func TestBrowserTestingRoutesToInvestigation(t *testing.T) {
	request := "test the frontend in a browser"
	if got := Classify(request); got != IntentInvestigate {
		t.Fatalf("Classify = %s, want investigate", got)
	}
	if got := BuildIntentContract(request); got.Kind != contract.ObjectiveInformational {
		t.Fatalf("contract kind = %s, want informational", got.Kind)
	}
}
