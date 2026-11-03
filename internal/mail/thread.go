package mail

import "strings"

func HeaderThreadID(messageID, inReplyTo, references string) string {
	if refs := strings.Fields(references); len(refs) != 0 {
		return CanonicalID(refs[0])
	}
	if inReplyTo != "" {
		return CanonicalID(inReplyTo)
	}
	return CanonicalID(messageID)
}

func ReplyReferences(messageID, inReplyTo, references string) string {
	refs := strings.Fields(references)
	if len(refs) == 0 {
		if parents := strings.Fields(inReplyTo); len(parents) == 1 {
			refs = parents
		}
	}
	refs = append(refs, messageID)
	canonical := make([]string, 0, len(refs))
	for _, ref := range refs {
		if id := CanonicalID(ref); id != "" {
			canonical = append(canonical, id)
		}
	}
	return strings.Join(canonical, " ")
}
