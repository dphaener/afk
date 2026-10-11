#!/usr/bin/env ruby
# frozen_string_literal: true

# Turns a afk plan into the `args` JSON object consumed by the Workflow
# engine scripts. Stdlib only, compatible with Ruby 2.6.
#
# Usage: plan_to_args.rb PLAN_PATH [--autonomous] [--models PATH]
#
# Each task's QA scenarios carry Tool, Case (happy | failure), Steps, Expected
# and Evidence. A task needs a happy scenario and a failure scenario, or the
# line `No failure case: {reason}` inside its QA Scenarios.
#
# Prints one JSON object to stdout and exits 0. On an invalid plan or
# unreadable input, prints `plan_to_args: <reason>` to stderr and exits 1.

require 'json'
require 'optparse'

class PlanError < StandardError; end

TIERS = %w[light balanced heavy reasoning].freeze
DEFAULT_TIER = 'balanced'
LABELS = ['What to do', 'Must NOT do', 'Files', 'Depends on', 'Acceptance Criteria', 'QA Scenarios', 'Model'].freeze
QA_LABEL = 'QA Scenarios'
# Scenario field keys. A line starting with one of these (or with `- Tool:`)
# opens or switches a field; any other line continues the field above it, so a
# colon or the word "Expected" inside a step cannot split a scenario.
QA_TOOL_LINE = /\A\s*-\s+Tool:\s*(.*?)\s*\z/.freeze
# The waiver for a task that cannot fail; matched before the continuation
# branch in parse_qa, or its reason would be glued onto the last Evidence.
QA_WAIVER_LINE = /\A\s*(?:-\s+)?No failure case:\s*(.*?)\s*\z/i.freeze
QA_CASES = %w[happy failure].freeze
QA_FIELD_LINE = /\A\s*(Steps|Case|Expected|Evidence):\s*(.*?)\s*\z/.freeze

TASK_LINE = /\A- \[([ x!])\] \((W(\d+)\.T(\d+))\) (.*?)\s*\z/.freeze
WAVE_LINE = /\A### Wave (\d+):\s*(.*?)\s*\z/.freeze
# Any markdown heading (a wave heading is matched first, so this closes a task
# block at "## " and at non-wave "### " headings alike).
HEADING_LINE = /\A\#{1,6} /.freeze
LABEL_LINE = /\A\s*\*\*([^*]+)\*\*:\s*(.*?)\s*\z/.freeze

def parse_models(path)
  raise PlanError, "cannot read models file #{path}" unless File.file?(path)

  section = File.read(path)[/^## Task Tiers\n(.*?)(?=^---|^## |\z)/m, 1]
  raise PlanError, "no Task Tiers table in #{path}" unless section

  tiers = {}
  section.each_line do |row|
    cells = row.split('|').map(&:strip)
    next unless cells.size >= 4

    tier = cells[1].delete('`')
    next unless TIERS.include?(tier)

    model = cells[2].delete('`')
    fallback = cells[3].delete('`')
    fallback = nil if fallback.empty? || fallback == "—"
    tiers[tier] = { 'model' => model, 'fallbackModel' => fallback }
  end
  missing = TIERS - tiers.keys
  raise PlanError, "models file #{path} lacks tiers: #{missing.join(', ')}" unless missing.empty?

  # Emit in canonical order.
  TIERS.each_with_object({}) { |t, h| h[t] = tiers[t] }
end

# Collects the subsection values of one task block as { label => joined value }.
# A value runs from its **Label**: line until the next label, blank line, or end of block.
def subsection_values(block_lines)
  values = {}
  current = nil
  in_fence = false
  block_lines.each do |line|
    if line =~ /\A\s*(```|~~~)/
      in_fence = !in_fence
      next
    end
    next if in_fence # a **Label**: inside a fenced example is not a subsection

    if (m = LABEL_LINE.match(line))
      label = m[1].strip
      if values.key?(label)
        current = nil
      else
        current = label
        values[current] = m[2]
      end
    elsif line.strip.empty?
      current = nil
    elsif current
      values[current] = "#{values[current]} #{line.strip}".strip
    end
  end
  values
end

# Splits a **Files** value into its entries as written, dropping the
# "(only if ...)" asides and surrounding backticks. Strictness lives in
# check_file_refs; this only tokenizes.
def parse_file_refs(value)
  return [] if value.nil?

  cleaned = value.gsub(/\s*\(only if[^)]*\)/i, '')
  cleaned.split(',').map { |s| s.strip.delete('`').strip }.reject(&:empty?)
end

# Drops a trailing line range (`path:107-125` or `path:107`) from a reference.
def strip_range(ref)
  ref.sub(/:\d+(?:-\d+)?\z/, '')
end

# Enforces contract C2 for **Files**: at least one exact file path, no N/A, no
# globs, and a directory (trailing slash) only beside an exact file. Exact paths
# let a task agent start editing at once instead of searching (PLAN section 5.7
# rule 8) and give execute.js's overlap check real paths to compare.
def check_file_refs(id, refs)
  raise PlanError, "(#{id}) **Files** is empty; name at least one exact file path" if refs.empty?

  refs.each do |r|
    if %w[n/a none].include?(r.downcase)
      raise PlanError, "(#{id}) **Files** must name exact file paths, not '#{r}'"
    end
    raise PlanError, "(#{id}) **Files** entry '#{r}' is a glob; name exact file paths" if r.include?('*')
  end
  exact = refs.reject { |r| r.end_with?('/') }
  return unless exact.empty?

  raise PlanError, "(#{id}) **Files** lists only directories; add at least one exact file path"
end

# Reads the QA scenarios of one task from its raw block lines (not from the
# joined subsection value, which loses line boundaries). The scenarios are the
# lines after the **QA Scenarios**: label up to the next label or blank line.
# Returns [scenarios, waiver]: scenarios are [{ 'tool', 'case', 'steps',
# 'expected', 'evidence' }] with missing fields as ''; waiver is the reason
# after `No failure case:` ('' when empty), or nil when there is no such line.
def parse_qa(block_lines)
  scenarios = []
  waiver = nil
  field = nil
  in_section = false
  in_fence = false
  block_lines.each do |line|
    if line =~ /\A\s*(```|~~~)/
      in_fence = !in_fence
      next
    end
    next if in_fence

    if (m = LABEL_LINE.match(line))
      in_section = m[1].strip == QA_LABEL && scenarios.empty?
      field = nil
      next
    end
    if line.strip.empty?
      in_section = false
      next
    end
    next unless in_section

    if (m = QA_WAIVER_LINE.match(line))
      waiver = m[1]
      field = nil
    elsif (m = QA_TOOL_LINE.match(line))
      scenarios << { 'tool' => m[1], 'case' => '', 'steps' => '', 'expected' => '', 'evidence' => '' }
      field = 'tool'
    elsif scenarios.any? && (m = QA_FIELD_LINE.match(line))
      field = m[1].downcase
      scenarios.last[field] = m[2]
    elsif scenarios.any? && field
      scenarios.last[field] = "#{scenarios.last[field]} #{line.strip}".strip
    end
  end
  scenarios.each do |sc|
    sc['evidence'] = sc['evidence'].delete('`').strip
    sc['case'] = sc['case'].delete('`').strip
  end
  [scenarios, waiver]
end

# Enforces contract C2 for **QA Scenarios**: at least one scenario, every field
# present, Case valid, evidence under .afk/{plan}/evidence/, and a happy
# scenario plus a failure scenario or a `No failure case:` waiver.
def check_qa(id, plan, scenarios, waiver)
  if scenarios.empty?
    raise PlanError, "(#{id}) has no **QA Scenarios**; add at least one scenario starting with '- Tool:' (N/A is not allowed)"
  end

  prefix = ".afk/#{plan}/evidence/"
  scenarios.each_with_index do |sc, i|
    %w[tool case steps expected evidence].each do |f|
      next unless sc[f].empty?

      raise PlanError, "(#{id}) QA scenario #{i + 1} is missing '#{f.capitalize}:'"
    end
  end
  scenarios.each_with_index do |sc, i|
    next if QA_CASES.include?(sc['case'])

    raise PlanError, "(#{id}) QA scenario #{i + 1} case '#{sc['case']}' must be happy or failure"
  end
  scenarios.each_with_index do |sc, i|
    ev = sc['evidence']
    if !ev.start_with?(prefix) || ev == prefix || ev.split('/').include?('..')
      raise PlanError, "(#{id}) QA scenario #{i + 1} evidence '#{ev}' must be a file under #{prefix}"
    end
  end

  cases = scenarios.map { |sc| sc['case'] }
  raise PlanError, "(#{id}) has no happy QA scenario; add one with 'Case: happy'" unless cases.include?('happy')
  if waiver && waiver.empty?
    raise PlanError, "(#{id}) 'No failure case:' needs a reason; say why nothing can fail"
  end
  return if cases.include?('failure') || waiver

  raise PlanError, "(#{id}) has no failure QA scenario; add one with 'Case: failure', " \
                   "or the line 'No failure case: {reason}' when nothing can fail"
end

def parse_depends(value)
  return [] if value.nil?

  items = value.split(',').map { |s| s.strip.delete('`').strip }.reject(&:empty?)
  items.reject { |s| %w[none n/a].include?(s.downcase) }
end

def parse_plan(lines)
  waves = []
  tasks = []
  current_wave = nil
  in_fence = false

  lines.each_with_index do |raw, idx|
    line = raw.chomp
    number = idx + 1
    if line =~ /\A\s*(```|~~~)/
      in_fence = !in_fence
      tasks.last[:lines] << line if tasks.last && !tasks.last[:closed]
      next
    end

    if !in_fence && (m = WAVE_LINE.match(line))
      current_wave = { 'number' => m[1].to_i, 'name' => m[2] }
      waves << current_wave
      tasks.last[:closed] = true if tasks.last
      next
    end

    if !in_fence && line =~ HEADING_LINE
      tasks.last[:closed] = true if tasks.last
      next
    end

    if !in_fence && (m = TASK_LINE.match(line))
      tasks.last[:closed] = true if tasks.last
      wave = current_wave ? current_wave['number'] : m[3].to_i
      tasks << {
        id: m[2], mark: m[1], head: m[5], wave: wave,
        start: number, end: number, lines: [], closed: false
      }
      next
    end

    next unless tasks.last && !tasks.last[:closed]

    tasks.last[:lines] << line
  end

  [waves, tasks]
end

def finalize_ends(tasks, waves_lines, total)
  # lineEnd is the line before the next task, wave heading, or "## " heading
  # (or the file's last line). Computed from the raw lines for exactness.
  starts = tasks.map { |t| t[:start] }
  tasks.each_with_index do |t, i|
    stop = total
    scan = t[:start] # 0-based index of the line after the task line
    in_fence = false
    while scan < total
      l = waves_lines[scan]
      if l =~ /\A\s*(```|~~~)/
        in_fence = !in_fence
      elsif !in_fence && (l =~ WAVE_LINE || l =~ HEADING_LINE || l =~ TASK_LINE)
        stop = scan
        break
      end
      scan += 1
    end
    stop = [stop, (starts[i + 1] ? starts[i + 1] - 1 : total)].min
    t[:end] = stop
  end
end

def build_tasks(raw_tasks, tiers, plan)
  raw_tasks.map do |t|
    status = { ' ' => 'todo', 'x' => 'done', '!' => 'failed' }.fetch(t[:mark])
    title = t[:head]
    title = title.sub(/\s*FAILED:.*\z/, '') if status == 'failed'
    values = subsection_values(t[:lines])

    tier = DEFAULT_TIER
    if values['Model'] && !values['Model'].empty?
      tier = values['Model'].split(/\s+/).first.delete('`').downcase
      raise PlanError, "task #{t[:id]} has unknown **Model** tier '#{tier}'" unless TIERS.include?(tier)
    end

    refs = parse_file_refs(values['Files'])
    check_file_refs(t[:id], refs)
    qa, waiver = parse_qa(t[:lines])
    check_qa(t[:id], plan, qa, waiver)

    {
      'id' => t[:id],
      'title' => title.strip,
      'wave' => t[:wave],
      'status' => status,
      'dependsOn' => parse_depends(values['Depends on']),
      'hasDepends' => values.key?('Depends on'),
      'files' => refs.map { |r| strip_range(r) }.uniq,
      'fileRefs' => refs,
      'qa' => qa,
      'tier' => tier,
      'model' => tiers[tier]['model'],
      'fallbackModel' => tiers[tier]['fallbackModel'],
      'lineStart' => t[:start],
      'lineEnd' => t[:end]
    }
  end
end

def validate_ids(tasks)
  seen = {}
  tasks.each do |t|
    raise PlanError, "duplicate task ID #{t['id']}" if seen[t['id']]

    seen[t['id']] = t
  end
  seen
end

def detect_mode(tasks)
  with = tasks.count { |t| t['hasDepends'] }
  return 'waves' if with.zero?
  return 'dag' if with == tasks.size

  without = tasks.reject { |t| t['hasDepends'] }.map { |t| t['id'] }
  raise PlanError, "mixed presence of **Depends on**: missing on #{without.join(', ')}"
end

def validate_dag(tasks, by_id)
  position = {}
  tasks.each_with_index { |t, i| position[t['id']] = i }
  tasks.each do |t|
    t['dependsOn'].each do |dep|
      target = by_id[dep]
      raise PlanError, "#{t['id']} depends on unknown task #{dep}" unless target
      raise PlanError, "#{t['id']} depends on #{dep} in the same wave" if target['wave'] == t['wave']
      raise PlanError, "#{t['id']} depends on #{dep} in a later wave" if target['wave'] > t['wave']
      # execute.js needs every dependency earlier in plan order, which the wave rules alone do not give a heading-less plan.
      if position[dep] > position[t['id']]
        raise PlanError, "#{t['id']} depends on #{dep}, which appears later in the plan"
      end
    end
  end

  # Defense in depth: the wave-order rules above already exclude cycles.
  state = {}
  visit = lambda do |id, path|
    return if state[id] == :done
    raise PlanError, "dependency cycle: #{(path + [id]).join(' -> ')}" if state[id] == :active

    state[id] = :active
    by_id[id]['dependsOn'].each { |dep| visit.call(dep, path + [id]) }
    state[id] = :done
  end
  tasks.each { |t| visit.call(t['id'], []) }
end

def synthesize_wave_deps(tasks)
  by_wave = {}
  tasks.each { |t| (by_wave[t['wave']] ||= []) << t['id'] }
  numbers = by_wave.keys.sort
  tasks.each do |t|
    previous = numbers.select { |n| n < t['wave'] }.max
    t['dependsOn'] = previous ? by_wave[previous].dup : []
  end
end

def run(argv)
  options = { autonomous: false, models: File.expand_path('../models.md', __dir__) }
  parser = OptionParser.new do |o|
    o.banner = 'Usage: plan_to_args.rb PLAN_PATH [--autonomous] [--models PATH]'
    o.on('--autonomous') { options[:autonomous] = true }
    o.on('--models PATH') { |p| options[:models] = p }
  end
  begin
    rest = parser.parse(argv)
  rescue OptionParser::ParseError => e
    raise PlanError, e.message
  end
  raise PlanError, 'expected exactly one PLAN_PATH argument' unless rest.size == 1

  plan_path = File.expand_path(rest.first)
  raise PlanError, "cannot read plan file #{plan_path}" unless File.file?(plan_path)

  tiers = parse_models(options[:models])
  lines = File.readlines(plan_path)
  waves, raw_tasks = parse_plan(lines)
  finalize_ends(raw_tasks, lines.map(&:chomp), lines.size)
  tasks = build_tasks(raw_tasks, tiers, File.basename(plan_path, '.*'))
  by_id = validate_ids(tasks)
  mode = detect_mode(tasks)
  if mode == 'dag'
    validate_dag(tasks, by_id)
  else
    synthesize_wave_deps(tasks)
  end
  tasks.each { |t| t.delete('hasDepends') }

  {
    'plan' => File.basename(plan_path, '.*'),
    'planPath' => plan_path,
    'pluginRoot' => File.expand_path('..', __dir__),
    'autonomous' => options[:autonomous],
    'mode' => mode,
    'tiers' => tiers,
    'verifierModel' => tiers['balanced']['model'],
    'waves' => waves,
    'tasks' => tasks
  }
end

begin
  puts JSON.generate(run(ARGV))
rescue PlanError => e
  warn "plan_to_args: #{e.message}"
  exit 1
end
