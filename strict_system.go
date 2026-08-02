package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type strictSystemRelocation struct {
	BlocksMoved int
	BytesMoved  int
}

func relocateMinimumClientSystem(body map[string]json.RawMessage) (strictSystemRelocation, error) {
	var system []json.RawMessage
	if errUnmarshal := json.Unmarshal(body["system"], &system); errUnmarshal != nil {
		return strictSystemRelocation{}, fmt.Errorf("decode system blocks: %w", errUnmarshal)
	}

	kept := make([]json.RawMessage, 0, len(system))
	movedTexts := make([]string, 0, len(system))
	result := strictSystemRelocation{}
	for _, rawBlock := range system {
		var block strictTextBlock
		if errUnmarshal := json.Unmarshal(rawBlock, &block); errUnmarshal != nil || block.Type != "text" {
			kept = append(kept, rawBlock)
			continue
		}
		if isStrictTopLevelSystemBlock(block.Text) {
			kept = append(kept, rawBlock)
			continue
		}
		if block.Text == "" {
			continue
		}
		movedTexts = append(movedTexts, block.Text)
		result.BlocksMoved++
		result.BytesMoved += len(block.Text)
	}
	if result.BlocksMoved == 0 {
		return result, nil
	}

	encodedSystem, errMarshal := json.Marshal(kept)
	if errMarshal != nil {
		return strictSystemRelocation{}, fmt.Errorf("encode retained system blocks: %w", errMarshal)
	}
	body["system"] = encodedSystem
	reminder := "<system-reminder>\n" + strings.Join(movedTexts, "\n\n") + "\n</system-reminder>"
	if errPrepend := prependStrictSystemReminder(body, reminder); errPrepend != nil {
		return strictSystemRelocation{}, errPrepend
	}
	return result, nil
}

func isStrictTopLevelSystemBlock(text string) bool {
	return text == identityPrompt ||
		text == strictHarnessPrompt ||
		strings.HasPrefix(text, "x-anthropic-billing-header:")
}

func prependStrictSystemReminder(body map[string]json.RawMessage, reminder string) error {
	var messages []json.RawMessage
	if rawMessages, exists := body["messages"]; exists && len(rawMessages) > 0 {
		if errUnmarshal := json.Unmarshal(rawMessages, &messages); errUnmarshal != nil {
			return fmt.Errorf("decode messages: %w", errUnmarshal)
		}
	}
	reminderBlock, _ := json.Marshal(strictTextBlock{Type: "text", Text: reminder})
	for index, rawMessage := range messages {
		var message map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(rawMessage, &message); errUnmarshal != nil {
			continue
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil || !strings.EqualFold(role, "user") {
			continue
		}
		content, errContent := prependStrictMessageContent(message["content"], reminderBlock)
		if errContent != nil {
			return fmt.Errorf("prepend reminder to first user message: %w", errContent)
		}
		message["content"] = content
		updatedMessage, errMarshal := json.Marshal(message)
		if errMarshal != nil {
			return fmt.Errorf("encode first user message: %w", errMarshal)
		}
		messages[index] = updatedMessage
		encodedMessages, errMarshalMessages := json.Marshal(messages)
		if errMarshalMessages != nil {
			return fmt.Errorf("encode messages: %w", errMarshalMessages)
		}
		body["messages"] = encodedMessages
		return nil
	}

	message, _ := json.Marshal(map[string]any{
		"role":    "user",
		"content": []json.RawMessage{reminderBlock},
	})
	messages = append([]json.RawMessage{message}, messages...)
	encodedMessages, errMarshal := json.Marshal(messages)
	if errMarshal != nil {
		return fmt.Errorf("encode reminder message: %w", errMarshal)
	}
	body["messages"] = encodedMessages
	return nil
}

func prependStrictMessageContent(rawContent, reminderBlock json.RawMessage) (json.RawMessage, error) {
	var text string
	if json.Unmarshal(rawContent, &text) == nil {
		blocks := []json.RawMessage{reminderBlock}
		if text != "" {
			textBlock, _ := json.Marshal(strictTextBlock{Type: "text", Text: text})
			blocks = append(blocks, textBlock)
		}
		return json.Marshal(blocks)
	}

	var blocks []json.RawMessage
	if len(rawContent) == 0 || string(rawContent) == "null" {
		blocks = []json.RawMessage{reminderBlock}
	} else if errUnmarshal := json.Unmarshal(rawContent, &blocks); errUnmarshal != nil {
		return nil, errUnmarshal
	} else {
		blocks = append([]json.RawMessage{reminderBlock}, blocks...)
	}
	return json.Marshal(blocks)
}
