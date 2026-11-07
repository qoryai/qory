package cmd

import "github.com/qoryai/qory/internal/machinestep"

// machineStepsFileFlag is the hidden flag of qory run that sets the path of the file
// that the machine's start script writes: the steps of the start of the machine the run
// runs on.
const machineStepsFileFlag = "machine-steps-file"

// machineStepsFileUsage is the help of --machine-steps-file, which --help does not show.
const machineStepsFileUsage = "with --run-code-file, write the steps of the machine's start, one JSON object per line in <file>, as the run's machine step"

// textMachineOnlyWithCode is the error of --machine-steps-file without --run-code-file.
const textMachineOnlyWithCode = "remove --" + machineStepsFileFlag + ": it applies only with --" + runCodeFileFlag

// writeMachineStep writes the machine step of run from the file at path, as
// [machinestep.Write] does, with each of its lines through said.
func writeMachineStep(run openedRun, path string, said func(string)) {
	machinestep.Write(func(kind, name string) (machinestep.Step, error) {
		return run.StartStep(kind, name)
	}, path, said)
}
