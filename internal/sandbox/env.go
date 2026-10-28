package sandbox

import "strings"

func mergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	result := append([]string{}, base...)
	for i, env := range result {
		key, _, ok := strings.Cut(env, "=")
		if ok {
			index[key] = i
		}
	}
	for key, value := range overlay {
		if strings.TrimSpace(key) == "" {
			continue
		}
		entry := key + "=" + value
		if i, ok := index[key]; ok {
			result[i] = entry
			continue
		}
		index[key] = len(result)
		result = append(result, entry)
	}
	return result
}
