# frozen_string_literal: true

# Tests for plan_to_args.rb. Stdlib only (minitest, open3, tmpdir). Each case
# writes a plan into a temp dir and runs the real script, so the exit code and
# the `plan_to_args: ...` message are checked the way execute.js sees them.
#
# Run: ruby plugins/afk/scripts/test/plan_to_args_test.rb

require 'minitest/autorun'
require 'open3'
require 'json'
require 'tmpdir'

SCRIPT = File.expand_path('../plan_to_args.rb', __dir__)
PLAN_NAME = 'demo'

# One task block. Keyword arguments override a subsection; nil omits it.
def task(id: 'W1.T1', mark: ' ', files: '`src/a.go`', depends: 'none', model: 'light',
         qa: "  - Tool: Bash\n    Steps: run `go test ./...`\n    Expected: exit 0\n    Evidence: `.afk/#{PLAN_NAME}/evidence/a.txt`")
  out = +"- [#{mark}] (#{id}) Do the thing\n\n"
  out << "  **What to do**: work\n\n  **Must NOT do**: nothing\n\n"
  out << "  **Files**: #{files}\n\n" if files
  out << "  **Depends on**: #{depends}\n\n" if depends
  out << "  **Acceptance Criteria**: it works\n\n"
  out << "  **QA Scenarios**:\n#{qa}\n\n" if qa
  out << "  **Model**: #{model}\n\n" if model
  out
end

def plan(*tasks)
  "# Demo\n\n## Waves\n\n### Wave 1: One\n\n#{tasks.join}\n## Success Criteria\n\n- [ ] ok\n"
end

class PlanToArgsTest < Minitest::Test
  def run_plan(text)
    Dir.mktmpdir do |dir|
      path = File.join(dir, "#{PLAN_NAME}.md")
      File.write(path, text)
      out, err, status = Open3.capture3('ruby', SCRIPT, path)
      return [status.exitstatus, out, err]
    end
  end

  def assert_rejected(text, fragment)
    code, _out, err = run_plan(text)
    assert_equal 1, code, err
    assert_match(/\Aplan_to_args: /, err)
    assert_includes err, fragment
  end

  # Load-bearing: the emitted shape execute.js and the QA check depend on, with
  # a range stripped from `files` but kept in `fileRefs`, and two scenarios.
  def test_valid_plan_emits_qa_files_and_refs
    qa = "  - Tool: Bash\n    Steps: run `go test`\n    Expected: ok\n    Evidence: `.afk/demo/evidence/one.txt`\n" \
         "  - Tool: Read\n    Steps: open the file and\n      look at the header\n    Expected: header present\n    Evidence: .afk/demo/evidence/two.txt"
    code, out, err = run_plan(plan(task(files: '`internal/state/plan.go:107-125`, `internal/state/testdata/`', qa: qa)))
    assert_equal 0, code, err
    t = JSON.parse(out)['tasks'].first
    assert_equal ['internal/state/plan.go', 'internal/state/testdata/'], t['files']
    assert_equal ['internal/state/plan.go:107-125', 'internal/state/testdata/'], t['fileRefs']
    assert_equal 2, t['qa'].size
    assert_equal({ 'tool' => 'Bash', 'steps' => 'run `go test`', 'expected' => 'ok',
                   'evidence' => '.afk/demo/evidence/one.txt' }, t['qa'][0])
    assert_equal 'open the file and look at the header', t['qa'][1]['steps']
    assert_equal '.afk/demo/evidence/two.txt', t['qa'][1]['evidence']
  end

  # Load-bearing: a colon and the word "Expected" inside a step must not split
  # the scenario, because fields are read line by line.
  def test_step_text_with_colon_and_expected_word
    qa = "  - Tool: Bash\n    Steps: run `x`; Expected: a prompt, then see: nothing\n    Expected: exit 0\n    Evidence: .afk/demo/evidence/a.txt"
    code, out, err = run_plan(plan(task(qa: qa)))
    assert_equal 0, code, err
    sc = JSON.parse(out)['tasks'].first['qa'].first
    assert_equal 'run `x`; Expected: a prompt, then see: nothing', sc['steps']
    assert_equal 'exit 0', sc['expected']
  end

  def test_directory_beside_exact_file_is_accepted
    code, out, err = run_plan(plan(task(files: '`a.go`, `testdata/golden/`')))
    assert_equal 0, code, err
    assert_equal ['a.go', 'testdata/golden/'], JSON.parse(out)['tasks'].first['files']
  end

  def test_rejections
    {
      'missing QA Scenarios subsection' => [plan(task(qa: nil)), '(W1.T1) has no **QA Scenarios**'],
      'QA Scenarios N/A' => [plan(task(qa: '  N/A')), 'N/A is not allowed'],
      'scenario missing Expected' => [plan(task(qa: "  - Tool: Bash\n    Steps: s\n    Evidence: .afk/demo/evidence/a.txt")), "missing 'Expected:'"],
      'scenario missing Evidence' => [plan(task(qa: "  - Tool: Bash\n    Steps: s\n    Expected: e")), "missing 'Evidence:'"],
      'evidence outside the plan dir' => [plan(task(qa: "  - Tool: Bash\n    Steps: s\n    Expected: e\n    Evidence: /tmp/a.txt")), 'must be a file under .afk/demo/evidence/'],
      'evidence for another plan' => [plan(task(qa: "  - Tool: Bash\n    Steps: s\n    Expected: e\n    Evidence: .afk/other/evidence/a.txt")), 'must be a file under .afk/demo/evidence/'],
      'evidence escaping with ..' => [plan(task(qa: "  - Tool: Bash\n    Steps: s\n    Expected: e\n    Evidence: .afk/demo/evidence/../x.txt")), 'must be a file under'],
      'Files subsection missing' => [plan(task(files: nil)), '(W1.T1) **Files** is empty'],
      'Files N/A' => [plan(task(files: 'N/A')), "not 'N/A'"],
      'Files glob' => [plan(task(files: '`src/*.go`')), 'is a glob'],
      'Files only a directory' => [plan(task(files: '`src/`')), 'only directories']
    }.each_value do |(text, fragment)|
      assert_rejected(text, fragment)
    end
  end

  # The pre-existing rules must keep holding after the QA change.
  def test_existing_rules_still_hold
    assert_rejected(plan(task, task), 'duplicate task ID W1.T1')
    assert_rejected(plan(task(model: 'huge')), "unknown **Model** tier 'huge'")
    assert_rejected(plan(task, task(id: 'W1.T2', depends: nil)), 'mixed presence of **Depends on**')
    assert_rejected(plan(task, task(id: 'W1.T2', depends: 'W1.T1')), 'in the same wave')
    backward = "#{plan(task(depends: 'W2.T1'))}\n### Wave 2: Two\n\n#{task(id: 'W2.T1')}"
    assert_rejected(backward, 'depends on W2.T1 in a later wave')
  end

  def test_failed_task_title_loses_reason
    code, out, err = run_plan(plan(task(mark: '!').sub('Do the thing', 'Do the thing FAILED: boom')))
    assert_equal 0, code, err
    t = JSON.parse(out)['tasks'].first
    assert_equal 'failed', t['status']
    assert_equal 'Do the thing', t['title']
  end
end
