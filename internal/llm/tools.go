package llm

func defaultToolParameters(parameters map[string]any) map[string]any {
	if len(parameters) == 0 {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": true,
		}
	}
	return parameters
}
