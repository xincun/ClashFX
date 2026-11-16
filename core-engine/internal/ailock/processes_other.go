//go:build !darwin && !windows

package ailock

func listOSProcesses() ([]ProcessEntry, error) {
	return nil, nil
}

func terminatePIDs(_ []int) int {
	return 0
}
