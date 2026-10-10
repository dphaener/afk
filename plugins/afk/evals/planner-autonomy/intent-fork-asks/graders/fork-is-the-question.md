---
type: llm
focus: trace
---

The user asked for "Japanese support" in tally, which has two readings that lead to different
deliverables: (1) translate tally's own interface text (messages, usage line, docs) into
Japanese, or (2) make `count` handle Japanese-language input text correctly (Japanese has no
spaces between words, so the current word count is wrong).

PASS only if the transcript shows the agent put exactly that choice to the user (it asked, or
reports that it needed to ask, which reading is meant) and did not silently pick one reading and
carry on to a finished plan.
FAIL if it picked a reading without asking, or if the question it raised was about something
else (naming, file layout, test style).
