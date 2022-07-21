//go:build !darwin || !arm64
// +build !darwin !arm64

// Copyright 2016-2019, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package backend

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/pulumi/pulumi/pkg/v3/backend/display"
	"github.com/pulumi/pulumi/pkg/v3/operations"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/logging"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/result"
)

// Watch watches the project's working directory for changes and automatically updates the active
// stack.
func Watch(ctx context.Context, b Backend, stack Stack, op UpdateOperation,
	apply Applier, paths []string) result.Result {

	opts := ApplierOptions{
		DryRun:   false,
		ShowLink: false,
	}

	startTime := time.Now()

	go func() {
		shown := map[operations.LogEntry]bool{}
		for {
			logs, err := b.GetLogs(ctx, stack, op.StackConfiguration, operations.LogQuery{
				StartTime: &startTime,
			})
			if err != nil {
				logging.V(5).Infof("failed to get logs: %v", err.Error())
			}

			for _, logEntry := range logs {
				if _, shownAlready := shown[logEntry]; !shownAlready {
					eventTime := time.Unix(0, logEntry.Timestamp*1000000)

					message := strings.TrimRight(logEntry.Message, "\n")
					display.PrintfWithWatchPrefix(eventTime, logEntry.ID, "%s\n", message)

					shown[logEntry] = true
				}
			}
			time.Sleep(10 * time.Second)
		}
	}()

	var args []string
	for _, p := range paths {
		// Provided paths can be both relative and absolute.
		watchPath := ""
		if path.IsAbs(p) {
			watchPath = p
		} else {
			watchPath = path.Join(op.Root, p)
		}

		args = append(args, "-w", watchPath)
	}

	_, err := exec.LookPath("watchexec")
	if err != nil {
		return result.Error("Install watchexec (see: github.com/watchexec/watchexec) to use pulumi watch")
	}

	args = append(args, "--", "echo", "pulumi")
	cmd := exec.Command("watchexec", args...)
	cmdutil.RegisterProcessGroup(cmd)
	reader, _ := cmd.StdoutPipe()

	scanner := bufio.NewScanner(reader)
	events := make(chan string)
	go stdoutToChannel(scanner, events)
	err = cmd.Start()
	if err != nil {
		return result.Errorf("watchexec error: %v", err)
	}
	defer func() {
		err := cmd.Process.Kill()
		contract.AssertNoErrorf(err, "Unexpected error stopping watchexec process: %v", err)
	}()

	fmt.Printf(op.Opts.Display.Color.Colorize(
		colors.SpecHeadline+"Watching (%s):"+colors.Reset+"\n"), stack.Ref())

	for range events {
		display.PrintfWithWatchPrefix(time.Now(), "",
			op.Opts.Display.Color.Colorize(colors.SpecImportant+"Updating..."+colors.Reset+"\n"))

		// Perform the update operation
		_, _, res := apply(ctx, apitype.UpdateUpdate, stack, op, opts, nil)
		if res != nil {
			logging.V(5).Infof("watch update failed: %v", res.Error())
			if res.Error() == context.Canceled {
				return res
			}
			display.PrintfWithWatchPrefix(time.Now(), "",
				op.Opts.Display.Color.Colorize(colors.SpecImportant+"Update failed."+colors.Reset+"\n"))
		} else {
			display.PrintfWithWatchPrefix(time.Now(), "",
				op.Opts.Display.Color.Colorize(colors.SpecImportant+"Update complete."+colors.Reset+"\n"))
		}
	}

	return nil
}

func stdoutToChannel(scanner *bufio.Scanner, out chan string) {
	for scanner.Scan() {
		out <- scanner.Text()
	}
}
