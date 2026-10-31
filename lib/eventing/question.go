package eventing

import (
	"encoding/json"
	"fmt"
)

// PermissionCapabilityQuestion marks a PermissionRequest that asks the user
// multiple-choice questions for the AskUser tool. The questions travel in
// Details under QuestionDetailsKey and the answers come back in the
// resolution's Metadata under QuestionAnswersKey. Allow means answered and
// deny means dismissed.
const PermissionCapabilityQuestion = "question"

const (
	QuestionDetailsKey = "questions"
	QuestionAnswersKey = "answers"
)

// Question is one multiple-choice question the model asks the user. Clients
// always offer a free-text "Other" answer in addition to Options.
type Question struct {
	Header      string           `json:"header"`
	Question    string           `json:"question"`
	MultiSelect bool             `json:"multi_select,omitempty"`
	Options     []QuestionOption `json:"options"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// QuestionAnswer is the user's answer to the Question at the same index.
type QuestionAnswer struct {
	Selected []string `json:"selected,omitempty"`
	// Other is the user's own free-text answer, if they wrote one.
	Other string `json:"other,omitempty"`
}

// QuestionDetails encodes questions for PermissionRequest.Details.
func QuestionDetails(questions []Question) map[string]string {
	data, _ := json.Marshal(questions)
	return map[string]string{QuestionDetailsKey: string(data)}
}

// DecodeQuestions returns the questions a question request asks.
func DecodeQuestions(request PermissionRequest) ([]Question, error) {
	if request.Capability != PermissionCapabilityQuestion {
		return nil, fmt.Errorf("expected capability %s", PermissionCapabilityQuestion)
	}
	var questions []Question
	if err := json.Unmarshal([]byte(request.Details[QuestionDetailsKey]), &questions); err != nil {
		return nil, fmt.Errorf("decode questions: %w", err)
	}
	return questions, nil
}

// QuestionAnswersMetadata encodes answers for PermissionResolution.Metadata.
func QuestionAnswersMetadata(answers []QuestionAnswer) map[string]string {
	data, _ := json.Marshal(answers)
	return map[string]string{QuestionAnswersKey: string(data)}
}

// DecodeQuestionAnswers returns the answers in a question resolution.
func DecodeQuestionAnswers(resolution PermissionResolution) ([]QuestionAnswer, error) {
	raw, ok := resolution.Metadata[QuestionAnswersKey]
	if !ok {
		return nil, fmt.Errorf("resolution has no answers")
	}
	var answers []QuestionAnswer
	if err := json.Unmarshal([]byte(raw), &answers); err != nil {
		return nil, fmt.Errorf("decode answers: %w", err)
	}
	return answers, nil
}
