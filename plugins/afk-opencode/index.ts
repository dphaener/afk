// afk for OpenCode: commands, the afk_session / afk_workflow tools, and
// the status.json awaiting_input handlers. See PROVENANCE.md.

import { dirname } from "node:path"
import { fileURLToPath } from "node:url"
import { assemble, COMMANDS, matchCommand, skillMeta } from "./lib/commands.mjs"
import { runWorkflow, scriptText, SCRIPTS } from "./lib/engine.mjs"
import { makeAgent } from "./lib/agent.mjs"
import { findTarget, transitions } from "./lib/status.mjs"

const ROOT = dirname(fileURLToPath(import.meta.url))
const DEFAULT_CONCURRENCY = 3

// Every plugin instance sees the same server event stream; handle each event once.
const seen: Set<string> = ((globalThis as any).__afkSeenEvents ??= new Set<string>())
function firstSighting(id: string | undefined) {
  if (!id) return true
  if (seen.has(id)) return false
  seen.add(id)
  if (seen.size > 2000) seen.delete(seen.values().next().value as string)
  return true
}

const list = (v: any) => (Array.isArray(v) ? v : Array.isArray(v?.data) ? v.data : [])

export default {
  id: "afk",
  async setup(ctx: any) {
    const log = (...a: unknown[]) => console.error("[afk]", ...a)

    // Root session (follow parentID) and its directory: a permission asked
    // inside a sub-agent's child session counts for the conducting session.
    async function rootOf(sessionID: string) {
      let s = await ctx.session.get({ sessionID })
      for (let i = 0; i < 20 && s?.parentID; i++) s = await ctx.session.get({ sessionID: s.parentID })
      return s ? { id: s.id as string, directory: s.location?.directory as string } : null
    }

    async function targetFor(sessionID: string) {
      const root = await rootOf(sessionID).catch(() => null)
      return root?.directory ? findTarget(root.directory, root.id) : null
    }

    async function knownModels() {
      const out = new Set<string>()
      try {
        for (const m of list(await ctx.model.list())) {
          if (!m?.providerID) continue
          if (m.id) out.add(`${m.providerID}/${m.id}`)
          if (m.modelID) out.add(`${m.providerID}/${m.modelID}`)
        }
      } catch (e) {
        log("model.list failed; skipping model validation", e)
      }
      return out
    }

    // ---- commands -------------------------------------------------------
    await ctx.command.transform((editor: any) => {
      for (const name of COMMANDS) {
        let meta: Record<string, string> = {}
        try {
          meta = skillMeta(ROOT, name)
        } catch {}
        const hint = meta["argument-hint"] ? ` ${meta["argument-hint"]}` : ""
        editor.add({
          name: `afk-${name}`,
          description: `${meta.description || `afk ${name}`}${hint}`,
          execute: async ({ sessionID, prompt, delivery }: any) => {
            await ctx.session.prompt({ ...prompt, sessionID, text: assemble(ROOT, name, prompt?.text), delivery })
          },
        })
      }
    })

    // ---- tools ----------------------------------------------------------
    await ctx.tool.transform((editor: any) => {
      editor.add({
        name: "afk_session",
        description:
          "afk: return this session's ID and working directory as JSON {sessionID, directory, title}. Pass `title` to rename this session first.",
        input: {
          type: "object",
          properties: { title: { type: "string", description: "New title for this session" } },
          additionalProperties: false,
        },
        options: { codemode: false },
        execute: async (input: any, context: any) => {
          if (input?.title) await ctx.session.update({ sessionID: context.sessionID, title: String(input.title) })
          const s = await ctx.session.get({ sessionID: context.sessionID })
          return {
            content: JSON.stringify({ sessionID: s.id, directory: s.location?.directory ?? null, title: s.title ?? null }),
          }
        },
      })

      editor.add({
        name: "afk_workflow",
        description:
          'afk: run a checked-in afk Workflow script (workflows/execute.js or workflows/review.js) with `args`. Each agent() call runs as a child session. Returns JSON {runID, ok, result, error?, logs, agents}. Only use when a afk prompt tells you to.',
        input: {
          type: "object",
          properties: {
            script: { type: "string", enum: Object.keys(SCRIPTS) },
            args: { type: "object", description: "The script's args as a real object, never a JSON string" },
            concurrency: { type: "integer", minimum: 1, description: `Max child sessions at once (default ${DEFAULT_CONCURRENCY})` },
            maxAgents: { type: "integer", minimum: 1, description: "Optional cap on total child sessions; exposed to the script as its budget" },
          },
          required: ["script", "args"],
          additionalProperties: false,
        },
        options: { codemode: false },
        execute: async (input: any, context: any) => {
          const runID = `fw_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`
          let args = input.args
          if (typeof args === "string") {
            // Models sometimes stringify; the scripts require an object.
            try {
              args = JSON.parse(args)
            } catch {}
          }
          const self = await ctx.session.get({ sessionID: context.sessionID })
          const agent = makeAgent({
            session: ctx.session,
            parentID: context.sessionID,
            directory: self.location?.directory,
            knownModels,
            onChild: ({ label, model }: any) => void context.progress({ status: `agent ${label}${model ? ` (${model})` : ""}` }).catch(() => {}),
          })
          const onAbort = () => void agent.abortAll()
          context.signal?.addEventListener("abort", onAbort, { once: true })
          try {
            const rec = await runWorkflow({
              text: scriptText(input.script),
              args,
              agent,
              concurrency: input.concurrency || DEFAULT_CONCURRENCY,
              maxAgents: input.maxAgents,
              signal: context.signal,
              onLog: (line: string) => void context.progress({ status: line }).catch(() => {}),
            })
            if (context.signal?.aborted) await agent.abortAll()
            const out: Record<string, unknown> = { runID, ok: rec.ok, result: rec.result ?? null }
            if (!rec.ok) out.error = rec.error
            Object.assign(out, { logs: rec.logs, agents: rec.agents, maxInFlight: rec.maxInFlight })
            return { content: JSON.stringify(out, null, 2) }
          } finally {
            context.signal?.removeEventListener("abort", onAbort)
          }
        },
      })
    })

    // ---- prompt hook: headless command expansion + C1 -------------------
    await ctx.session.hook("prompt", async (event: any) => {
      const hit = matchCommand(event.prompt?.text)
      if (hit) {
        try {
          event.prompt.text = assemble(ROOT, hit.name, hit.args)
        } catch (e) {
          log("command expansion failed", e)
        }
      }
      try {
        const s = await ctx.session.get({ sessionID: event.sessionID })
        // Child-session prompts come from afk itself, not the user.
        if (s?.parentID || !s?.location?.directory) return
        transitions.userInput(findTarget(s.location.directory, s.id))
      } catch {}
    })

    // ---- event handlers: S1a, S1b, permission clear, C1 ------------------
    const controller = new AbortController()
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
          const type = event?.type
          if (!["permission.asked", "permission.replied", "form.created", "form.replied"].includes(type)) continue
          if (!firstSighting(event.id)) continue
          const data = event.data || {}
          const sessionID = data.sessionID ?? data.form?.sessionID
          if (!sessionID) continue
          try {
            const target = await targetFor(sessionID)
            if (type === "permission.asked") transitions.permissionAsked(target)
            else if (type === "permission.replied") transitions.permissionReplied(target)
            else if (type === "form.created") transitions.questionShown(target)
            else transitions.userInput(target)
          } catch (e) {
            log(`handler ${type} failed`, e)
          }
        }
      } catch (e) {
        if (!controller.signal.aborted) log("event stream ended", e)
      }
    })()

    return () => controller.abort()
  },
}
