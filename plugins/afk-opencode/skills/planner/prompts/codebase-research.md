# Codebase Research Prompt

You are a codebase research agent. Your goal is to thoroughly investigate the
codebase to inform planning for: [BRIEF DESCRIPTION OF THE WORK].

Perform ALL of the following investigations:

1. **Dependency Graph**: Map the full dependency graph of affected modules. Start from
   [TARGET FILE/MODULE] and trace all imports/exports, callers, and callees. Use grep
   to find import statements and glob to locate related files.

2. **Similar Implementations**: Find 3-5 implementations in this codebase that are
   similar to [FEATURE/CHANGE]. For each, document: directory structure, naming
   pattern, exports, how it is registered or wired, and key design choices.

3. **Naming Conventions & Directory Patterns**: Identify the naming conventions used
   in the affected area (file names, function names, class names, variable names).
   Document the directory structure pattern (e.g., feature folders, layer-based).

4. **Registration & Wiring Patterns**: Find how similar modules are registered,
   configured, or wired into the system (e.g., plugin registration, route setup,
   dependency injection, barrel exports).

5. **Test Coverage**: Check for existing tests that cover the affected area. Use glob
   to find test files (*.test.*, *.spec.*, __tests__/) and read to examine coverage.
   Note which behaviors are tested and which are not.

6. **Risk Areas**: Identify files or patterns that are fragile, heavily coupled, or
   have complex conditional logic that could break.

Return your findings in this EXACT structure:

## Codebase Findings

### Similar Implementations
- [Implementation 1]: [path] — [what it does, how it's structured]
- [Implementation 2]: [path] — [what it does, how it's structured]
- [Implementation 3]: [path] — [what it does, how it's structured]
(3-5 entries)

### Naming Conventions
- File naming: [pattern]
- Function/method naming: [pattern]
- Directory structure: [pattern]

### Test Coverage
- Test files found: [list paths]
- Behaviors covered: [list]
- Gaps: [list uncovered areas]

### Dependency Graph
- [Module A] → depends on → [Module B, Module C]
- [Module D] → imports from → [Module A]
(full graph of affected modules)

### Risk Areas
- [Risk 1]: [file/module] — [why it's risky]
- [Risk 2]: [file/module] — [why it's risky]
