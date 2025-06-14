/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	wstats "github.com/Microsoft/hcsshim/cmd/containerd-shim-runhcs-v1/stats"
	"github.com/basuotian/containerd/cmd/ctr/commands"
	v1 "github.com/containerd/cgroups/v3/cgroup1/stats"
	v2 "github.com/containerd/cgroups/v3/cgroup2/stats"
	"github.com/containerd/typeurl/v2"
	"github.com/urfave/cli/v2"
)

const (
	formatFlag  = "format"
	formatTable = "table"
	formatJSON  = "json"
)

var metricsCommand = &cli.Command{
	Name:      "metrics",
	Usage:     "Get a single data point of metrics for a task with the built-in Linux runtime",
	ArgsUsage: "CONTAINER",
	Aliases:   []string{"metric"},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  formatFlag,
			Usage: `"table" or "json"`,
			Value: formatTable,
		},
	},
	Action: func(cliContext *cli.Context) error {
		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()
		container, err := client.LoadContainer(ctx, cliContext.Args().First())
		if err != nil {
			return err
		}
		task, err := container.Task(ctx, nil)
		if err != nil {
			return err
		}
		metric, err := task.Metrics(ctx)
		if err != nil {
			return err
		}

		var data interface{}
		switch {
		case typeurl.Is(metric.Data, (*v1.Metrics)(nil)):
			data = &v1.Metrics{}
		case typeurl.Is(metric.Data, (*v2.Metrics)(nil)):
			data = &v2.Metrics{}
		case typeurl.Is(metric.Data, (*wstats.Statistics)(nil)):
			data = &wstats.Statistics{}
		default:
			return errors.New("cannot convert metric data to cgroups.Metrics or windows.Statistics")
		}
		if err := typeurl.UnmarshalTo(metric.Data, data); err != nil {
			return err
		}

		switch cliContext.String(formatFlag) {
		case formatTable:
			w := tabwriter.NewWriter(os.Stdout, 1, 8, 4, ' ', 0)
			fmt.Fprintf(w, "ID\tTIMESTAMP\t\n")
			fmt.Fprintf(w, "%s\t%s\t\n\n", metric.ID, metric.Timestamp)
			switch v := data.(type) {
			case *v1.Metrics:
				printCgroupMetricsTable(w, v)
			case *v2.Metrics:
				printCgroup2MetricsTable(w, v)
			case *wstats.Statistics:
				printWindowsStats(w, v)
			}
			return w.Flush()
		case formatJSON:
			marshaledJSON, err := json.MarshalIndent(data, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(marshaledJSON))
			return nil
		default:
			return errors.New("format must be table or json")
		}
	},
}

func printCgroupMetricsTable(w *tabwriter.Writer, data *v1.Metrics) {
	fmt.Fprintf(w, "METRIC\tVALUE\t\n")
	if data.Memory != nil {
		fmt.Fprintf(w, "memory.usage_in_bytes\t%d\t\n", data.Memory.Usage.Usage)
		fmt.Fprintf(w, "memory.limit_in_bytes\t%d\t\n", data.Memory.Usage.Limit)
		fmt.Fprintf(w, "memory.stat.cache\t%d\t\n", data.Memory.TotalCache)
	}
	if data.CPU != nil {
		fmt.Fprintf(w, "cpuacct.usage\t%d\t\n", data.CPU.Usage.Total)
		fmt.Fprintf(w, "cpuacct.usage_percpu\t%v\t\n", data.CPU.Usage.PerCPU)
	}
	if data.Pids != nil {
		fmt.Fprintf(w, "pids.current\t%v\t\n", data.Pids.Current)
		fmt.Fprintf(w, "pids.limit\t%v\t\n", data.Pids.Limit)
	}
}

func printCgroup2MetricsTable(w *tabwriter.Writer, data *v2.Metrics) {
	fmt.Fprintf(w, "METRIC\tVALUE\t\n")
	if data.Pids != nil {
		fmt.Fprintf(w, "pids.current\t%v\t\n", data.Pids.Current)
		fmt.Fprintf(w, "pids.limit\t%v\t\n", data.Pids.Limit)
	}
	if data.CPU != nil {
		fmt.Fprintf(w, "cpu.usage_usec\t%v\t\n", data.CPU.UsageUsec)
		fmt.Fprintf(w, "cpu.user_usec\t%v\t\n", data.CPU.UserUsec)
		fmt.Fprintf(w, "cpu.system_usec\t%v\t\n", data.CPU.SystemUsec)
		fmt.Fprintf(w, "cpu.nr_periods\t%v\t\n", data.CPU.NrPeriods)
		fmt.Fprintf(w, "cpu.nr_throttled\t%v\t\n", data.CPU.NrThrottled)
		fmt.Fprintf(w, "cpu.throttled_usec\t%v\t\n", data.CPU.ThrottledUsec)
	}
	if data.Memory != nil {
		fmt.Fprintf(w, "memory.usage\t%v\t\n", data.Memory.Usage)
		fmt.Fprintf(w, "memory.usage_limit\t%v\t\n", data.Memory.UsageLimit)
		fmt.Fprintf(w, "memory.swap_usage\t%v\t\n", data.Memory.SwapUsage)
		fmt.Fprintf(w, "memory.swap_limit\t%v\t\n", data.Memory.SwapLimit)
	}
}

func printWindowsStats(w *tabwriter.Writer, windowsStats *wstats.Statistics) {
	if windowsStats.GetLinux() != nil {
		stats := windowsStats.GetLinux()
		printCgroupMetricsTable(w, stats)
	} else if windowsStats.GetWindows() != nil {
		printWindowsContainerStatistics(w, windowsStats.GetWindows())
	}
	// Print VM stats if its isolated
	if windowsStats.VM != nil {
		printWindowsVMStatistics(w, windowsStats.VM)
	}
}

func printWindowsContainerStatistics(w *tabwriter.Writer, stats *wstats.WindowsContainerStatistics) {
	fmt.Fprintf(w, "METRIC\tVALUE\t\n")
	fmt.Fprintf(w, "timestamp\t%s\t\n", stats.Timestamp)
	fmt.Fprintf(w, "start_time\t%s\t\n", stats.ContainerStartTime)
	fmt.Fprintf(w, "uptime_ns\t%d\t\n", stats.UptimeNS)
	if stats.Processor != nil {
		fmt.Fprintf(w, "cpu.total_runtime_ns\t%d\t\n", stats.Processor.TotalRuntimeNS)
		fmt.Fprintf(w, "cpu.runtime_user_ns\t%d\t\n", stats.Processor.RuntimeUserNS)
		fmt.Fprintf(w, "cpu.runtime_kernel_ns\t%d\t\n", stats.Processor.RuntimeKernelNS)
	}
	if stats.Memory != nil {
		fmt.Fprintf(w, "memory.commit_bytes\t%d\t\n", stats.Memory.MemoryUsageCommitBytes)
		fmt.Fprintf(w, "memory.commit_peak_bytes\t%d\t\n", stats.Memory.MemoryUsageCommitPeakBytes)
		fmt.Fprintf(w, "memory.private_working_set_bytes\t%d\t\n", stats.Memory.MemoryUsagePrivateWorkingSetBytes)
	}
	if stats.Storage != nil {
		fmt.Fprintf(w, "storage.read_count_normalized\t%d\t\n", stats.Storage.ReadCountNormalized)
		fmt.Fprintf(w, "storage.read_size_bytes\t%d\t\n", stats.Storage.ReadSizeBytes)
		fmt.Fprintf(w, "storage.write_count_normalized\t%d\t\n", stats.Storage.WriteCountNormalized)
		fmt.Fprintf(w, "storage.write_size_bytes\t%d\t\n", stats.Storage.WriteSizeBytes)
	}
}

func printWindowsVMStatistics(w *tabwriter.Writer, stats *wstats.VirtualMachineStatistics) {
	fmt.Fprintf(w, "METRIC\tVALUE\t\n")
	if stats.Processor != nil {
		fmt.Fprintf(w, "vm.cpu.total_runtime_ns\t%d\t\n", stats.Processor.TotalRuntimeNS)
	}
	if stats.Memory != nil {
		fmt.Fprintf(w, "vm.memory.working_set_bytes\t%d\t\n", stats.Memory.WorkingSetBytes)
		fmt.Fprintf(w, "vm.memory.virtual_node_count\t%d\t\n", stats.Memory.VirtualNodeCount)
		fmt.Fprintf(w, "vm.memory.available\t%d\t\n", stats.Memory.VmMemory.AvailableMemory)
		fmt.Fprintf(w, "vm.memory.available_buffer\t%d\t\n", stats.Memory.VmMemory.AvailableMemoryBuffer)
		fmt.Fprintf(w, "vm.memory.reserved\t%d\t\n", stats.Memory.VmMemory.ReservedMemory)
		fmt.Fprintf(w, "vm.memory.assigned\t%d\t\n", stats.Memory.VmMemory.AssignedMemory)
		fmt.Fprintf(w, "vm.memory.slp_active\t%t\t\n", stats.Memory.VmMemory.SlpActive)
		fmt.Fprintf(w, "vm.memory.balancing_enabled\t%t\t\n", stats.Memory.VmMemory.BalancingEnabled)
		fmt.Fprintf(w, "vm.memory.dm_operation_in_progress\t%t\t\n", stats.Memory.VmMemory.DmOperationInProgress)
	}
}
