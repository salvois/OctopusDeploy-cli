package wait

import (
	"fmt"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc/v2"
	"github.com/OctopusDeploy/cli/pkg/cmd"
	"github.com/OctopusDeploy/cli/pkg/constants"
	"github.com/OctopusDeploy/cli/pkg/factory"
	"github.com/OctopusDeploy/cli/pkg/util"
	"github.com/OctopusDeploy/go-octopusdeploy/v2/pkg/client"
	"github.com/OctopusDeploy/go-octopusdeploy/v2/pkg/tasks"
	"github.com/spf13/cobra"
)

const (
	FlagTimeout    = "timeout"
	FlagProgress   = "progress"
	DefaultTimeout = 600
)

type WaitOptions struct {
	*cmd.Dependencies
	TaskIDs                []string
	GetServerTasksCallback ServerTasksCallback
	GetTaskDetailsCallback TaskDetailsCallback
	Timeout               int
	ShowProgress         bool
}

type ServerTasksCallback func([]string) ([]*tasks.Task, error)
type TaskDetailsCallback func(string) (*tasks.TaskDetailsResource, error)

func NewWaitOps(dependencies *cmd.Dependencies, taskIDs []string) *WaitOptions {
	return &WaitOptions{
		Dependencies:           dependencies,
		TaskIDs:               taskIDs,
		GetServerTasksCallback: GetServerTasksCallback(dependencies.Client),
		GetTaskDetailsCallback: GetTaskDetailsCallback(dependencies.Client),
		Timeout:               DefaultTimeout,
		ShowProgress:         false,
	}
}

func NewCmdWait(f factory.Factory) *cobra.Command {
	var timeout int
	var showProgress bool
	cmd := &cobra.Command{
		Use:     "wait [TaskIDs]",
		Short:   "Wait for task(s) to finish",
		Long:    "Wait for a provided list of task(s) to finish",
		Example: heredoc.Docf("$ %s task wait", constants.ExecutableName),
		RunE: func(c *cobra.Command, args []string) error {
			taskIDs := make([]string, len(args))
			copy(taskIDs, args)

			taskIDs = append(taskIDs, util.ReadValuesFromPipe()...)

			dependencies := cmd.NewDependencies(f, c)
			opts := NewWaitOps(dependencies, taskIDs)
			opts.Timeout = timeout
			opts.ShowProgress = showProgress

			return WaitRun(opts)
		},
	}

	flags := cmd.Flags()
	flags.IntVar(&timeout, FlagTimeout, DefaultTimeout, "Duration to wait (in seconds) before stopping execution")
	flags.BoolVar(&showProgress, FlagProgress, false, "Show detailed progress of the tasks")

	return cmd
}

func WaitRun(opts *WaitOptions) error {
	if len(opts.TaskIDs) == 0 {
		return fmt.Errorf("no server task IDs provided, at least one is required")
	}

	if opts.ShowProgress && len(opts.TaskIDs) > 1 {
		return fmt.Errorf("--progress flag is only supported when waiting for a single task")
	}

	tasks, err := opts.GetServerTasksCallback(opts.TaskIDs)
	if err != nil {
		return err
	}

	if len(tasks) == 0 {
		return fmt.Errorf("no server tasks found")
	}

	pendingTaskIDs := make([]string, 0)
	failedTaskIDs := make([]string, 0)
	formatter := NewTaskOutputFormatter(opts.Out)

	for _, t := range tasks {
		if t.IsCompleted == nil || !*t.IsCompleted {
			pendingTaskIDs = append(pendingTaskIDs, t.ID)
		}
		if (t.IsCompleted != nil && *t.IsCompleted) && (t.FinishedSuccessfully != nil && !*t.FinishedSuccessfully) {
			failedTaskIDs = append(failedTaskIDs, t.ID)
		}

		formatter.PrintTaskInfo(t)
	}

	if len(pendingTaskIDs) == 0 {
		if len(failedTaskIDs) != 0 {
			return fmt.Errorf("One or more deployment tasks failed: %s", strings.Join(failedTaskIDs, ", "))
		}
		return nil
	}

	gotError := make(chan error, 1)
	done := make(chan bool, 1)
	completedChildIds := make(map[string]bool)

	go func() {
		for len(pendingTaskIDs) != 0 {
			time.Sleep(5 * time.Second)
			tasks, err = opts.GetServerTasksCallback(pendingTaskIDs)
			if err != nil {
				gotError <- err
				return
			}
			for _, t := range tasks {
				if opts.ShowProgress {
					details, err := opts.GetTaskDetailsCallback(t.ID)
					if err != nil {
						continue // Skip progress display if we can't get details
					}

					if len(details.ActivityLogs) > 0 {
						// Process all activities
						for _, activity := range details.ActivityLogs {
							formatter.PrintActivityElement(activity, 0, completedChildIds)
						}
					}
				}

				if t.IsCompleted != nil && *t.IsCompleted {
					if t.FinishedSuccessfully != nil && !*t.FinishedSuccessfully {
						failedTaskIDs = append(failedTaskIDs, t.ID)
					}
					formatter.PrintTaskInfo(t)
					pendingTaskIDs = removeTaskID(pendingTaskIDs, t.ID)
				}
			}
		}
		if len(failedTaskIDs) != 0 {
			gotError <- fmt.Errorf("One or more deployment tasks failed: %s", strings.Join(failedTaskIDs, ", "))
			return
		}
		done <- true
	}()

	select {
	case <-done:
		return nil
	case err := <-gotError:
		return err
	case <-time.After(time.Duration(opts.Timeout) * time.Second):
		return fmt.Errorf("timeout while waiting for pending tasks")
	}
}

func GetServerTasksCallback(octopus *client.Client) ServerTasksCallback {
	return func(taskIDs []string) ([]*tasks.Task, error) {
		query := tasks.TasksQuery{
			IDs: taskIDs,
		}

		resourceTasks, err := octopus.Tasks.Get(query)
		if err != nil {
			return nil, err
		}

		tasks, err := resourceTasks.GetAllPages(octopus.Sling())
		if err != nil {
			return nil, err
		}

		return tasks, nil
	}
}

func GetTaskDetailsCallback(octopus *client.Client) TaskDetailsCallback {
	return func(taskID string) (*tasks.TaskDetailsResource, error) {
		return tasks.GetDetails(octopus, octopus.GetSpaceID(), taskID)
	}
}

func removeTaskID(taskIDs []string, taskID string) []string {
	for i, p := range taskIDs {
		if p == taskID {
			taskIDs[i] = taskIDs[len(taskIDs)-1]
			taskIDs = taskIDs[:len(taskIDs)-1]
			break
		}
	}
	return taskIDs
}
