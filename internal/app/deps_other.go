//go:build !windows

package app

// Реестр есть только на Windows.
func regCollect(found []string, keys []string, vkey string) []RegValue { return nil }

func regApply(job *Job, vals []RegValue, senderHome string, batch *elevBatch, overwrite bool) (done []string, problems []string) {
	return nil, nil
}

func regQueryText(key string) string { return "" }
