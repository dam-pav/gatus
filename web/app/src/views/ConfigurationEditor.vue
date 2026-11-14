<template>
  <section class="container mx-auto max-w-7xl px-4 py-8 text-slate-900 dark:text-slate-100">
    <div class="mb-6 flex items-center justify-between gap-4">
      <div class="min-w-0 flex-1">
        <h2 class="text-2xl font-bold">Configuration editor</h2>
        <p class="mt-1 text-sm text-slate-600 dark:text-slate-400">Edit YAML with live schema validation. Saves are checked against the full configuration and applied within 30 seconds.</p>
      </div>
      <div class="flex shrink-0 items-center gap-4">
        <router-link
          to="/"
          title="Return"
          aria-label="Return"
          class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-slate-900 transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:text-slate-100"
        >
          <Undo2 class="h-5 w-5" aria-hidden="true" />
        </router-link>
      </div>
    </div>
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <label for="config-file" class="text-sm font-medium">File</label>
      <select id="config-file" :value="selected" :disabled="busy" @change="changeFile" class="max-w-full rounded-md border border-slate-300 bg-white p-2 text-sm dark:border-slate-700 dark:bg-slate-900">
        <option v-for="file in files" :key="file.name" :value="file.name">{{ file.name }}</option>
      </select>
      <Button variant="ghost" size="icon" title="Reload" aria-label="Reload" :disabled="busy" @click="reload">
        <RefreshCw class="h-5 w-5" aria-hidden="true" />
      </Button>
      <Button variant="ghost" size="icon" title="Format" aria-label="Format" :disabled="busy || !view" @click="format">
        <AlignLeft class="h-5 w-5" aria-hidden="true" />
      </Button>
      <Button variant="ghost" size="icon" title="Copy" aria-label="Copy" :disabled="busy || !view" @click="copy">
        <Copy class="h-5 w-5" aria-hidden="true" />
      </Button>
      <Button variant="ghost" size="icon" title="Validate" aria-label="Validate" :disabled="busy || !view || localErrors.length > 0" @click="validateServer">
        <ListChecks class="h-5 w-5" aria-hidden="true" />
      </Button>
      <Button variant="ghost" size="icon" :title="saving ? 'Saving…' : 'Save configuration'" :aria-label="saving ? 'Saving…' : 'Save configuration'" :disabled="busy || !dirty || !serverValid || localErrors.length > 0" @click="save">
        <LoaderCircle v-if="saving" class="h-5 w-5 animate-spin" aria-hidden="true" />
        <Save v-else class="h-5 w-5" aria-hidden="true" />
      </Button>
    </div>
    <p v-if="error" role="alert" class="mb-4 whitespace-pre-wrap rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200">{{ error }}</p>
    <p role="status" aria-live="polite" class="mb-3 text-sm text-slate-600 dark:text-slate-400">{{ status }}</p>
    <div ref="editorElement" :inert="busy || undefined" class="yaml-editor overflow-hidden rounded-lg border border-slate-300 bg-white dark:border-slate-700 dark:bg-slate-950" />
    <ul v-if="localErrors.length" class="mt-4 space-y-1 text-sm text-red-700 dark:text-red-300" aria-label="Validation errors">
      <li v-for="(issue, index) in localErrors" :key="index">
        <button class="text-left hover:underline" @click="jumpTo(issue.from)">Line {{ issue.line }}: {{ issue.message }}</button>
      </li>
    </ul>
    <p class="mt-4 text-xs text-slate-500 dark:text-slate-400">Configuration may contain secrets. Environment variable references are preserved; Gatus resolves them during server validation. Reload discards your draft.</p>
  </section>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { Undo2, RefreshCw, AlignLeft, Copy, ListChecks, Save, LoaderCircle } from 'lucide-vue-next'
import { basicSetup, EditorView } from 'codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags } from '@lezer/highlight'
import { linter, lintGutter, forceLinting } from '@codemirror/lint'
import { parseDocument, LineCounter } from 'yaml'
import Ajv from 'ajv'
import { Button } from '@/components/ui/button'

const editorElement = ref(null)
const files = ref([])
const selected = ref('')
const content = ref('')
const revision = ref('')
const busy = ref(true)
const saving = ref(false)
const error = ref('')
const status = ref('Loading configuration…')
const localErrors = ref([])
const serverValid = ref(false)
const dirty = computed(() => content.value !== files.value.find(f => f.name === selected.value)?.content)
let view = null
let schemaValidate = null
let timer = null
let validationSequence = 0
let destroyed = false

async function request(path = '', method = 'GET', body) {
  const response = await fetch(`/api/v1/config/editor${path}`, {
    method, credentials: 'include', cache: 'no-store',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!response.ok) {
    const message = await response.json().catch(() => ({}))
    throw new Error(message.error || (response.status === 404 ? 'Configuration editor is disabled. Set GATUS_CONFIG_EDITOR=true on the server.' : `Request failed (${response.status}). Check your authentication and try again.`))
  }
  return response.json()
}

function inspect(text) {
  const lines = new LineCounter()
  const doc = parseDocument(text, { lineCounter: lines, uniqueKeys: true, merge: true })
  const issues = doc.errors.map(e => ({ from: e.pos?.[0] || 0, message: e.message }))
  if (!issues.length && schemaValidate) {
    try {
      const data = doc.contents === null ? {} : doc.toJS({ maxAliasCount: 100 })
      schemaValidate(data)
      for (const issue of schemaValidate.errors || []) {
        const path = issue.instancePath.split('/').slice(1).map(part => part.replace(/~1/g, '/').replace(/~0/g, '~'))
        const value = path.reduce((value, key) => value?.[key], data)
        // The server resolves environment variables before checking their type.
        if (typeof value === 'string' && /\$(?:\{[^}]+\}|[A-Za-z_][A-Za-z0-9_]*)/.test(value)) continue
        if (issue.keyword === 'additionalProperties') path.push(issue.params.additionalProperty)
        const node = path.length ? doc.getIn(path, true) : doc.contents
        const from = node?.range?.[0] || 0
        issues.push({ from, message: `${path.join('.') || 'config'} ${issue.message}` })
      }
    } catch (e) { issues.push({ from: 0, message: e.message }) }
  }
  return issues.map(issue => ({ ...issue, line: lines.linePos(issue.from).line }))
}

function diagnostics() {
  localErrors.value = inspect(view.state.doc.toString())
  return localErrors.value.map(issue => ({ from: issue.from, to: issue.from, severity: 'error', message: issue.message }))
}

function updated() {
  content.value = view.state.doc.toString()
  serverValid.value = false
  error.value = ''
  status.value = 'Checking configuration…'
  validationSequence++
  clearTimeout(timer)
  timer = setTimeout(validateServer, 700)
}

function applySnapshot(snapshot, name = selected.value) {
  files.value = snapshot.files
  revision.value = snapshot.revision
  selected.value = snapshot.files.some(f => f.name === name) ? name : snapshot.files[0]?.name || ''
  content.value = snapshot.files.find(f => f.name === selected.value)?.content || ''
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: content.value } })
  forceLinting(view)
}

async function reload() {
  if (dirty.value && files.value.length && !window.confirm('Discard unsaved changes and reload?')) return
  busy.value = true
  error.value = ''
  validationSequence++
  try {
    const snapshot = await request()
    if (destroyed) return
    applySnapshot(snapshot)
    await validateServer()
  } catch (e) { if (!destroyed) { error.value = e.message; status.value = 'Could not load configuration.' } }
  finally { if (!destroyed) busy.value = false }
}

function changeFile(event) {
  if (dirty.value && !window.confirm('Discard unsaved changes and switch files?')) { event.target.value = selected.value; return }
  selected.value = event.target.value
  applySnapshot({ files: files.value, revision: revision.value })
}

async function validateServer() {
  clearTimeout(timer)
  const sequence = ++validationSequence
  serverValid.value = false
  localErrors.value = inspect(content.value)
  if (localErrors.value.length) { status.value = 'Fix the highlighted YAML or schema errors.'; return }
  if (!selected.value) return
  status.value = 'Validating full configuration…'
  try {
    await request('/validate', 'POST', { file: selected.value, content: content.value, revision: revision.value })
    if (destroyed || sequence !== validationSequence) return
    serverValid.value = true
    error.value = ''
    status.value = 'YAML, schema, and Gatus configuration checks passed.'
  } catch (e) {
    if (destroyed || sequence !== validationSequence) return
    error.value = e.message
    status.value = 'Configuration validation failed.'
  }
}

async function save() {
  busy.value = saving.value = true
  clearTimeout(timer)
  validationSequence++
  try {
    const snapshot = await request('', 'PUT', { file: selected.value, content: content.value, revision: revision.value })
    if (destroyed) return
    applySnapshot(snapshot)
    clearTimeout(timer)
    validationSequence++
    serverValid.value = true
    error.value = ''
    status.value = 'Saved. Gatus will reload the configuration within 30 seconds.'
  } catch (e) { if (!destroyed) { error.value = e.message; status.value = 'Configuration was not saved.' } }
  finally { if (!destroyed) busy.value = saving.value = false }
}

function format() {
  const doc = parseDocument(content.value, { uniqueKeys: true, merge: true })
  if (doc.errors.length) { error.value = 'Fix YAML syntax errors before formatting.'; return }
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: doc.toString({ indent: 2 }) } })
}
async function copy() {
  try { await navigator.clipboard.writeText(content.value); status.value = 'Copied configuration to clipboard.' }
  catch { error.value = 'Could not copy. Select the YAML and copy it with your keyboard.' }
}
function jumpTo(from) { view.dispatch({ selection: { anchor: from }, scrollIntoView: true }); view.focus() }
function beforeUnload(event) { if (dirty.value && files.value.length) { event.preventDefault(); event.returnValue = '' } }
onBeforeRouteLeave(() => !dirty.value || !files.value.length || window.confirm('Leave and discard unsaved configuration changes?'))

onMounted(async () => {
  view = new EditorView({
    parent: editorElement.value,
    extensions: [basicSetup, yaml(), lintGutter(), linter(diagnostics),
      syntaxHighlighting(HighlightStyle.define([
        { tag: tags.propertyName, class: 'yaml-key' },
        { tag: tags.string, class: 'yaml-string' },
        { tag: [tags.bool, tags.number, tags.null], class: 'yaml-value' },
        { tag: tags.comment, class: 'yaml-comment' },
      ])),
      EditorView.contentAttributes.of({ 'aria-label': 'YAML configuration', spellcheck: 'false' }),
      EditorView.updateListener.of(update => { if (update.docChanged) updated() }),
      EditorView.theme({ '&': { fontSize: '14px' }, '.cm-scroller': { overflow: 'auto', minHeight: '480px', maxHeight: '70vh' }, '.cm-content': { fontFamily: 'monospace' } }),
    ],
  })
  window.addEventListener('beforeunload', beforeUnload)
  try {
    const schema = await request('/schema')
    if (destroyed) return
    schemaValidate = new Ajv({ allErrors: true, strict: false }).compile(schema)
    await reload()
  } catch (e) { if (!destroyed) { error.value = e.message; status.value = 'Could not initialize editor.'; busy.value = false } }
})
onBeforeUnmount(() => {
  destroyed = true
  validationSequence++
  clearTimeout(timer)
  window.removeEventListener('beforeunload', beforeUnload)
  view?.destroy()
})
</script>

<style>
.yaml-editor .yaml-key { color: #1d4ed8; }
.yaml-editor .yaml-string { color: #047857; }
.yaml-editor .yaml-value { color: #7e22ce; }
.yaml-editor .yaml-comment { color: #64748b; }
.dark .yaml-editor .yaml-key { color: #93c5fd; }
.dark .yaml-editor .yaml-string { color: #6ee7b7; }
.dark .yaml-editor .yaml-value { color: #d8b4fe; }
.dark .yaml-editor .yaml-comment { color: #94a3b8; }
.dark .yaml-editor .cm-editor { color: #e2e8f0; background: #020617; }
.dark .yaml-editor .cm-gutters { color: #94a3b8; background: #0f172a; border-color: #334155; }
.dark .yaml-editor .cm-activeLine { background: rgb(148 163 184 / 10%); }
.dark .yaml-editor .cm-activeLineGutter { background: #1e293b; }
.dark .yaml-editor .cm-cursor { border-left-color: #e2e8f0; }
.dark .yaml-editor .cm-tooltip { color: #e2e8f0; background: #0f172a; border-color: #475569; }
.dark .yaml-editor .cm-selectionBackground, .dark .yaml-editor .cm-focused .cm-selectionBackground { background: #1e40af !important; }
.dark .yaml-editor ::selection { color: #e2e8f0; }
</style>
