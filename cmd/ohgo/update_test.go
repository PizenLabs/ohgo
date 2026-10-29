package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/update"
	"github.com/spf13/cobra"
)

type fakeUpdateService struct {
	status    update.Status
	result    update.Result
	checkErr  error
	updateErr error
	checks    int
	updates   int
}

func (f *fakeUpdateService) Check(context.Context) (update.Status, error) {
	f.checks++
	return f.status, f.checkErr
}

func (f *fakeUpdateService) Update(context.Context) (update.Result, error) {
	f.updates++
	return f.result, f.updateErr
}

func updateTestCommand() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{Use: "update"}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return cmd, &out
}

func TestExecuteUpdateReportsSuccessOnlyAfterInstallation(t *testing.T) {
	cmd, out := updateTestCommand()
	svc := &fakeUpdateService{result: update.Result{InstalledVersion: "v2.0.0"}}
	if err := executeUpdate(cmd, svc, false); err != nil {
		t.Fatalf("executeUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "Updated ohgo to v2.0.0.") {
		t.Fatalf("output = %q", out.String())
	}
	if svc.updates != 1 || svc.checks != 0 {
		t.Fatalf("service calls = checks %d updates %d", svc.checks, svc.updates)
	}
}

func TestExecuteUpdateAlreadyCurrentIsNotAnInstall(t *testing.T) {
	cmd, out := updateTestCommand()
	svc := &fakeUpdateService{result: update.Result{AlreadyCurrent: true}}
	if err := executeUpdate(cmd, svc, false); err != nil {
		t.Fatalf("executeUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestExecuteUpdateNamesTheOwningPackageManager(t *testing.T) {
	cmd, out := updateTestCommand()
	svc := &fakeUpdateService{updateErr: &update.ManagedError{
		Manager: "Homebrew",
		Command: "brew upgrade ohgo",
	}}
	err := executeUpdate(cmd, svc, false)
	var reported *reportedExit
	if !errors.As(err, &reported) {
		t.Fatalf("error = %v, want reportedExit", err)
	}
	if !strings.Contains(out.String(), "managed by Homebrew") || !strings.Contains(out.String(), "brew upgrade ohgo") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestExecuteUpdateRefusalsAreExplainedWithoutDuplication(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"unsupported platform": {err: update.ErrUnsupportedPlatform, want: "not supported for this platform"},
		"development build":    {err: update.ErrDevelopmentVersion, want: "development version"},
		"concurrent update":    {err: update.ErrUpdateBusy, want: "already running"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, out := updateTestCommand()
			svc := &fakeUpdateService{updateErr: test.err}
			err := executeUpdate(cmd, svc, false)
			var reported *reportedExit
			if !errors.As(err, &reported) {
				t.Fatalf("error = %v, want reportedExit", err)
			}
			if !strings.Contains(out.String(), test.want) {
				t.Fatalf("output = %q, want %q", out.String(), test.want)
			}
		})
	}
}

func TestExecuteUpdateKeepsUnexpectedFailureCause(t *testing.T) {
	cmd, _ := updateTestCommand()
	boom := errors.New("checksum mismatch: artifact differs")
	svc := &fakeUpdateService{updateErr: boom}
	err := executeUpdate(cmd, svc, false)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapped cause", err)
	}
	var reported *reportedExit
	if errors.As(err, &reported) {
		t.Fatal("an ordinary failure must not be reported as a handled refusal")
	}
}

func TestExecuteUpdateCheckDoesNotInstall(t *testing.T) {
	cmd, out := updateTestCommand()
	svc := &fakeUpdateService{status: update.Status{
		CurrentVersion:    "v1.0.0",
		LatestVersion:     "v1.1.0",
		Available:         true,
		Reason:            update.ReasonAvailable,
		PlatformSupported: true,
	}}
	if err := executeUpdate(cmd, svc, true); err != nil {
		t.Fatalf("executeUpdate: %v", err)
	}
	if svc.updates != 0 {
		t.Fatalf("--check performed %d updates", svc.updates)
	}
	if !strings.Contains(out.String(), "v1.0.0") || !strings.Contains(out.String(), "v1.1.0") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestUpdateCommandRefusesDevelopmentBuildOffline(t *testing.T) {
	var out bytes.Buffer
	cmd := updateCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	var reported *reportedExit
	if !errors.As(err, &reported) {
		t.Fatalf("error = %v, want reportedExit for a development build", err)
	}
	if !strings.Contains(out.String(), "development version") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestUpdateChecksEnabledPrecedence(t *testing.T) {
	disabled := false
	enabled := true
	cases := []struct {
		name     string
		settings config.Settings
		env      map[string]string
		want     bool
	}{
		{name: "default on", want: true},
		{name: "environment off", env: map[string]string{"OHGO_UPDATE_CHECK": "off"}, want: false},
		{name: "environment on beats config", settings: config.Settings{Update: &config.UpdateSettings{Enabled: &disabled}}, env: map[string]string{"OHGO_UPDATE_CHECK": "on"}, want: true},
		{name: "config off", settings: config.Settings{Update: &config.UpdateSettings{Enabled: &disabled}}, want: false},
		{name: "config on", settings: config.Settings{Update: &config.UpdateSettings{Enabled: &enabled}}, want: true},
		{name: "ci disables", env: map[string]string{"CI": "true"}, want: false},
		{name: "explicit environment on beats ci", env: map[string]string{"CI": "true", "OHGO_UPDATE_CHECK": "on"}, want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(key string) string { return test.env[key] }
			if got := updateChecksEnabled(test.settings, getenv); got != test.want {
				t.Fatalf("updateChecksEnabled = %v, want %v", got, test.want)
			}
		})
	}
}
