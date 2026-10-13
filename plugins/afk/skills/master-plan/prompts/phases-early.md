# The Early Phases: brief, survey, model, rounds

Each phase ends by writing its file and running `afk plan advance`. If the command refuses, it says what is missing; do that, then run it again.

## Phase `brief` — what the owner wants

The file: `.afk/plan/brief.md`.

If `$ARGUMENTS` holds text, that is the owner's request. If it is empty, ask one plain question in your message, with no options: what do they want to build, and what made them want it? Wait for the answer. Do not ask anything else yet; the survey and the rounds come later, and a question asked now has nothing to stand on.

Save the owner's words to `.afk/plan/brief.md` exactly as they wrote them: the same words, the same order, their spelling. Do not tidy, shorten or restate. If they wrote in two or three messages, save all of them, in order. The brief is the one record of what was asked before anything was decided, and the critic later reads it to find parts of the request no task covers, so a brief in your words would hide your own misreadings. Once saved, the file is never edited; if the owner adds to their request later, that becomes a question and an answer in `questions.jsonl`, not a change to the brief.

Then run `afk plan advance`.

## Phase `survey` — what is already known

The file: `.afk/plan/survey.md`.

Read before you ask. In this order:

1. **The owner profile.** Run `afk profile`. It prints the profile's path, any rule files, and then one entry per line as `<strength>  <topic>: <value>`, with `[<kind>]` and `— <reason>` when they are set. A `rule` must be followed; a `default` is used unless the project says otherwise; a value of `no preference` means recommend one and say why. When there is no profile yet, it says so and exits 0; that is fine, and it means every setup fact will have to be asked.
   - **A non-zero exit means stop.** The profile has a problem, and the command prints the problems instead of the entries. Tell the owner to run `afk profile check`, fix what it lists, and then say so. Never plan from part of a profile: a missing rule is a guessed fact.
   - Once the kind of project is plain from the brief (`web-app`, `cli`, `library`, or whatever the owner's entries use), run `afk profile --kind <kind>` too. An entry for that kind beats a general entry on the same topic. If the kind is not plain yet, it is a round-one question; run the kind-specific command then, before the round's other questions are settled.
2. **The rule files** the profile lists. Read each one in full. They are the owner's house rules and the plan has to follow them, but they are read, never copied into the plan.
3. **The folder.** Look at what is in the project folder: an existing README, code, a `package.json` or `go.mod`, a `.github/workflows/` folder, an `AGENTS.md` or `CLAUDE.md`. An empty folder with only `.git` and `.afk` is the usual case for a new project, and that is a fact worth one line. Use a fact look-up sub-agent when the folder is large.
4. **Nothing else.** Do not read a sibling project, a folder above this one or the owner's other repositories, however similar they look, unless the owner names one. What a sibling project did is not a fact about this one.

Write `.afk/plan/survey.md`: what you read (each file or folder, one line, with what it told you), the profile entries that apply to this project (every one, with its strength), the rule files you read, and which setup facts the profile does not cover and will have to be asked. This file is for the resumed session and the owner, so it is short and plain.

Then run `afk plan advance`.

## Phase `model` — what the words mean

The file: `CONTEXT.md` in the project's root, which is the project's glossary. (This phase has no file under `.afk/plan/`; `afk plan advance` moves it on when you run it.)

Before asking about features, settle what the project's nouns mean, with the owner. A plan whose terms are vague gets built three different ways by three different tasks.

- Start from the brief. List the terms it uses that name a thing in the project (a "note", a "member", a "booking", a "feed"). For each, say in one sentence what you take it to mean, and ask the owner whether that is right. Do this in the message, as plain questions, a few at a time.
- **Challenge a vague or overloaded term when it is used.** If "user" could mean both the person who signs up and the person they invite, say so and ask which is which, and whether they need different words. If "post" is used for the thing written and the act of writing it, split them.
- **Try each relationship against an invented edge case.** When the owner says a booking belongs to a member, ask what happens when the member leaves: does the booking go with them, stay, or move? One such case per relationship, chosen to be the one most likely to show a gap. An edge case the owner settles now is a task that does not go wrong later.
- **Write each settled term into `CONTEXT.md` at once**, as it is settled, not at the end. One entry per term: the term, one or two sentences of meaning, and the relationships to other terms. If `CONTEXT.md` already exists, add to it with Edit and keep what is there.
- **Meanings only.** The glossary says what a thing *is*, never how it is stored, which table holds it, or which page shows it. "A booking is a member's claim on one slot of one court for one hour" belongs; "bookings are rows in the bookings table" does not.

Stop when every term in the brief has an entry and the owner has agreed with each. For a small project that is five or six terms and a few minutes. Then run `afk plan advance`.

## Phase `rounds` — the questions

The file: `.afk/plan/questions.jsonl`.

This is where rules 2 and 3 are applied. Every choice the plan depends on that neither the brief, the profile, the folder nor the glossary has settled is asked here, in rounds.

### The two kinds

- **A product question** is a choice about what the thing is or does. It is asked through `AskUserQuestion` with options and one recommendation, worded so that the owner can accept it by choosing the first option. Put the recommended option first and mark it `(recommended)`; give two to four options in all; say in the question why you recommend what you do, in a few words. The owner can always type something else, and what they type is their answer.
- **A setup question** is a fact about how the owner builds, hosts or names things that the profile does not hold: the project's name, where it will run, which accounts exist, the address of a machine. It is asked as a plain question in your message, with no options and no `AskUserQuestion`, because the owner knows the answer and a list of guesses only gets in the way. Several setup questions go in one message, numbered.

Never turn a setup fact into a multiple-choice question, and never present a product choice without a recommendation.

### Rounds

A round holds every question whose prerequisites are settled. Ask them all, product questions through `AskUserQuestion` and setup questions in the message, and wait. A question that depends on an answer still open waits for the next round. Keep going until there is no question left whose answer the plan needs. Most projects take two rounds, three at most; a round with one question in it means the previous round should have held it.

**Round one always holds:**

1. Who the project is for (a product question: for example only the owner, their household, a club, the public), with a recommendation drawn from the brief.
2. Whether it has an interface, and of what kind (a product question: a web page, a command line, an API only, none), with a recommendation. This question always has the id `r1-interface`, and its recorded answer always starts with `yes` or `no`, then a colon and the kind in the owner's words: `yes: a web page`, `yes: a phone app`, `no: a command line`, `no: an API only`, `no: none`. **Yes means screens a person looks at**, which a design tool could draw; a command line, an API only or none is `no`. When the owner's answer does not make that plain, ask them which it is; do not decide for them.
3. The profile entries being applied, shown as `afk profile` printed them, with the plain question whether any of them does not fit this project. The owner's answer is recorded as a setup question with the id `r1-profile`. An entry the owner overrides is used the way they say for this project, and the plan's Decisions table later says the profile said otherwise.

Plus every setup fact `survey.md` listed as not covered, and every product question that needs nothing else settled first.

The id and the form of the interface answer are fixed because code reads them: `afk plan advance` takes the first word of that answer to decide whether the session has a `design` phase after `check`. It refuses to leave `rounds` while no question has the id `r1-interface`, or while its answer starts with anything but `yes` or `no`, and says which. When it says the answer is in the wrong form and what the owner meant is plain, rewrite the line's answer in the fixed form without asking again.

### Offering a fact back to the profile

When the owner answers a setup question with something that is about how they build things in general and not only about this project (their deploy tool, their hosting, their usual database, where their projects live), offer it back so they are not asked again next time. Show the exact command, for example:

```
afk profile set hosting "Fly.io" --kind web-app --reason "what I already use"
```

and ask whether to run it. Run it only when they say yes, through Bash, and show what it printed. Do not offer back a fact that belongs to this project alone (its name, its domain), and never offer back anything that looks like a key or token.

### Recording questions

Every question is one line of `.afk/plan/questions.jsonl`, written **when it is asked**, with an empty answer, and rewritten **when it is answered**. Write the line first and ask second, so a session that stops between the two still knows the question was asked. One JSON object per line, these keys and no others:

| Key | Holds |
|---|---|
| `id` | A short name unique in the file, with the round in it: `r1-audience`, `r2-auth`. Never reused. |
| `round` | The round number, from 1. |
| `kind` | `product` or `setup`. |
| `question` | The question as asked. |
| `options` | For a product question, the options as offered, the recommended one first. For a setup question, `[]`. |
| `recommended` | For a product question, the recommended option, word for word one of `options`. For a setup question, `""`. |
| `answer` | Empty until answered; then what the owner chose or wrote. |
| `answered_at` | Empty until answered; then the time, RFC 3339 in UTC, such as `2026-10-13T14:02:00Z`. |

A line that does not follow this is read as no question at all, and `afk plan status` reports it as a problem. When you answer a line, replace the whole line with Edit, matching on its `"id"`. When the owner reworded a recommendation rather than choosing it, the answer is their wording. The one answer with a fixed form is `r1-interface`: `yes: <kind>` or `no: <kind>`, with the kind in the owner's words.

### Leaving the phase

`afk plan advance` moves on as soon as every question in the file has an answer and the `r1-interface` answer starts with `yes` or `no`. So it is your call when the rounds are over, not the command's: run it only when there is no question left to ask. If an answer opens a new question, that is the next round; ask it first.
