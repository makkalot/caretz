## Structured agent calls

Use `AgentSession.RunStructured` when an agent needs to return a typed JSON result. It describes the target's JSON fields to the model, decodes the reply, checks `validate:"required"` fields, and gives the model one chance to correct invalid output. The target is updated only after validation succeeds.

```go
type ExpenseResult struct {
	Category string    `json:"category" validate:"required"`
	Amount   float64   `json:"amount" validate:"required"`
	Date     time.Time `json:"date" validate:"required"`
	Notes    string    `json:"notes" validate:"required"`
}

var expense ExpenseResult
_, err := session.RunStructured(ctx, "Extract the expense from this receipt", &expense)
if err != nil {
	return err
}
// expense now contains a validated result.
```

The caller can still put task-specific rules in the prompt. `/goal` uses this method for its evidence planner, task planner, checker, and judge responses.
