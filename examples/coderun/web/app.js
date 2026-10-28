'use strict';
const $ = id => document.getElementById(id);
let project = null, stream = null, running = false, ready = false, selectedFile = '', epoch = 0;
const cards = new Map(), calls = new Map();
const commandHistory = new Map();
let errorTimer;
let startedAt = 0, lastUpdate = 0, phase = '', reasoningText = '';
function activity(text) {
  phase = text; lastUpdate = Date.now(); updateActivity();
}
function updateActivity() {
  $('progress').hidden = !running;
  if (!running || !ready) return;
  const elapsed = Math.max(0, Math.floor((Date.now() - startedAt) / 1000));
  const quiet = Math.floor((Date.now() - lastUpdate) / 1000);
  $('activity').textContent = `${phase || 'Waiting for model'} · ${elapsed}s elapsed${quiet >= 10 ? ` · ${quiet}s since last update` : ''}`;
  $('connection').textContent = `${phase || 'Waiting for model'} · ${elapsed}s`;
}
setInterval(updateActivity, 1000);
function showError(error) {
  $('error').textContent = error.message || String(error);
  $('error').hidden = false;
  clearTimeout(errorTimer);
  errorTimer = setTimeout(() => { $('error').hidden = true; }, 9000);
}
async function api(url, method = 'GET', body) {
  const response = await fetch(url, { method, headers: body === undefined ? {} : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
  if (!response.ok) {
    const detail = await response.json().catch(() => ({}));
    throw new Error(detail.error || `Request failed (${response.status})`);
  }
  return response.headers.get('content-type')?.includes('application/json') ? response.json() : response.text();
}
function endpoint(suffix) { return `/api/projects/${encodeURIComponent(project.id)}/${suffix}`; }
function controls() {
  $('prompt').disabled = $('send').disabled = $('run').disabled = !project || !ready || running;
  $('cli-command').disabled = $('execute-command').disabled = !project || !ready || running;
  $('stop').disabled = $('command-stop').disabled = !running || !ready;
}
function message(role, text) {
  const item = document.createElement('div'); item.className = `message ${role}`;
  const label = document.createElement('strong'); label.textContent = role === 'user' ? 'You' : 'Agent';
  item.append(label, document.createTextNode(text)); $('messages').append(item);
  $('messages').scrollTop = $('messages').scrollHeight;
}
function renderHistory(list) {
  $('messages').replaceChildren(); calls.clear();
  for (const entry of list || []) message(entry.role, entry.text);
}
async function projects() {
  const list = await api('/api/projects'); $('project-list').replaceChildren();
  for (const entry of list) {
    const button = document.createElement('button'); button.textContent = entry.name;
    button.classList.toggle('selected', entry.id === project?.id);
    button.onclick = () => selectProject(entry); $('project-list').append(button);
  }
  return list;
}
function selectProject(entry) {
  epoch++; stream?.close(); project = entry; running = false; ready = false; selectedFile = '';
  startedAt = 0; phase = ''; reasoningText = ''; $('progress').hidden = true;
  $('reasoning').hidden = $('live-response').hidden = true;
  $('reasoning-content').textContent = $('live-text').textContent = '';
  cards.clear(); calls.clear(); $('permissions').replaceChildren(); $('messages').replaceChildren();
  $('file-list').replaceChildren(); $('file-name').textContent = 'File preview'; $('file-content').textContent = 'Select a file to view its contents.';
  $('output').textContent = "Your app's output will appear here.";
  $('command-status').textContent = ''; renderCommandHistory();
  $('project-title').textContent = entry.name; $('connection').textContent = 'Connecting…';
  localStorage.setItem('coderun-project', entry.id); controls(); projects().catch(showError);
  const generation = epoch;
  stream = new EventSource(endpoint('events'));
  stream.onmessage = event => {
    if (generation !== epoch) return;
    try { handleFrame(JSON.parse(event.data)); } catch (error) { showError(error); }
  };
  stream.onopen = () => {
    if (generation !== epoch) return;
    // The history frame is the connection handshake; controls enable after it.
    cards.clear(); $('permissions').replaceChildren();
    refreshFiles().catch(showError);
  };
  stream.onerror = () => { if (generation === epoch) { ready = false; controls(); $('connection').textContent = 'Reconnecting…'; } };
  activateTab('chat');
}
function toolFrame(f) {
  let row = calls.get(f.call_id);
  if (!row) {
    row = document.createElement('details'); const summary = document.createElement('summary');
    const content = document.createElement('pre'); row.append(summary, content);
    if (f.tool === 'Bash') row.open = true;
    calls.set(f.call_id, row); $('messages').append(row);
  }
  let args = {};
  try { args = JSON.parse(f.arguments || '{}'); } catch { /* Raw arguments remain visible below. */ }
  const target = args.command || args.file_path || args.path || args.pattern || '';
  const description = `${f.tool}${target ? ` ${target}` : ''}`;
  row.firstChild.textContent = description + (f.kind === 'tool_result' ? ' · finished' : ' · working');
  row.lastChild.textContent = f.kind === 'tool_result' ? [f.arguments, f.text].filter(Boolean).join('\n\n') : f.arguments || '';
  if (f.kind === 'tool_call') activity(`Running ${description}`);
  else activity(`${description} finished; waiting for model`);
  if (f.kind === 'tool_result' && f.tool === 'Bash') $('output').textContent = f.text || '(No output returned.)';
}
function permission(request) {
  if (cards.has(request.request_id)) return;
  const card = document.createElement('section'); card.className = 'permission';
  const title = document.createElement('strong'); title.textContent = `Permission: ${request.tool_name} · ${request.capability}`;
  const detail = document.createElement('p'); detail.textContent = [request.target, request.command, request.reason, request.preview].filter(Boolean).join('\n');
  const expiry = document.createElement('small'); const buttons = document.createElement('div'); buttons.className = 'row';
  for (const [decision, label] of [['allow', 'Approve'], ['allow_session', 'Approve for session'], ['deny', 'Deny']]) {
    if (decision === 'allow_session' && request.no_session_grant) continue;
    const button = document.createElement('button'); button.textContent = label;
    const url = endpoint(`permissions/${encodeURIComponent(request.request_id)}`);
    button.onclick = async () => {
      const generation = epoch;
      for (const b of buttons.children) b.disabled = true;
      try { await api(url, 'POST', { decision }); if (generation === epoch) removePermission(request.request_id); }
      catch (error) { showError(error); for (const b of buttons.children) b.disabled = false; }
    };
    buttons.append(button);
  }
  card.append(title, detail, expiry, buttons); $('permissions').append(card);
  cards.set(request.request_id, { card, expiry, request }); updateExpiries();
}
function removePermission(id) { cards.get(id)?.card.remove(); cards.delete(id); }
function updateExpiries() {
  for (const { card, expiry, request } of cards.values()) {
    const seconds = Math.max(0, Math.ceil((Date.parse(request.expires_at) - Date.now()) / 1000));
    expiry.textContent = seconds ? `Expires in ${Math.floor(seconds / 60)}m ${seconds % 60}s` : 'Expired';
    if (!seconds) for (const b of card.querySelectorAll('button')) b.disabled = true;
  }
}
setInterval(updateExpiries, 1000);
function handleFrame(f) {
  if (f.kind === 'history') { renderHistory(f.history); ready = true; controls(); }
  if (f.kind === 'user' || f.kind === 'assistant') message(f.kind, f.text);
  if (f.kind === 'assistant_stream') {
    $('live-response').hidden = !f.text;
    $('live-text').textContent = f.text || '';
    if (f.text) { activity('Receiving response'); $('live-text').scrollTop = $('live-text').scrollHeight; }
  }
  if (f.kind === 'reasoning') {
    if (f.started) { reasoningText = ''; $('reasoning-content').textContent = ''; $('reasoning').hidden = true; activity('Waiting for model'); }
    if (f.text) reasoningText = f.text;
    else if (f.delta) reasoningText += f.delta;
    if (f.text || f.delta) {
      $('reasoning').hidden = false; $('reasoning-content').textContent = reasoningText;
      $('reasoning-content').scrollTop = $('reasoning-content').scrollHeight;
      activity('Model is reasoning');
    }
    if (f.done) activity('Receiving response');
  }
  if (f.kind === 'tool_call' || f.kind === 'tool_result') {
    toolFrame(f);
    if (f.kind === 'tool_result' && ['Write', 'Edit'].includes(f.tool)) refreshFiles().catch(showError);
  }
  if (f.kind === 'permission') { permission(f.permission); activity('Waiting for your permission'); activateTab('chat'); }
  if (f.kind === 'permission_resolved') removePermission(f.permission.request_id);
  if (typeof f.running === 'boolean') {
    running = f.running; controls();
    if (running) {
      startedAt = Date.parse(f.started_at) || startedAt || Date.now();
      if (f.command) { activity(`Running ${f.command}`); $('command-status').textContent = 'Running…'; }
      else if (!phase) activity('Waiting for model');
    } else {
      $('connection').textContent = f.kind === 'turn_error' ? 'Turn failed' : 'Ready';
      $('live-response').hidden = true;
    }
    updateActivity();
    if (f.command && !running) renderCommandResult(f);
    if (f.kind === 'turn_error') showError(f.text);
    if (!running) refreshFiles().catch(showError);
  } else if (f.kind === 'status') { activity(f.text || 'Waiting for model'); if (f.text) message('assistant', f.text); }
}
async function refreshFiles() {
  if (!project) return;
  const generation = epoch, list = await api(endpoint('files'));
  if (generation !== epoch) return;
  $('file-list').replaceChildren();
  for (const name of list) {
    const button = document.createElement('button'); button.textContent = name;
    button.onclick = () => viewFile(name).catch(showError); $('file-list').append(button);
  }
  if (!list.length) $('file-list').textContent = 'No files yet.';
  if (selectedFile && list.includes(selectedFile)) await viewFile(selectedFile);
}
async function viewFile(name) {
  const generation = epoch; selectedFile = name;
  const text = await api(endpoint(`files/${name.split('/').map(encodeURIComponent).join('/')}`));
  if (generation !== epoch || selectedFile !== name) return;
  $('file-name').textContent = name; $('file-content').textContent = text;
}
async function start(suffix, body) {
  const generation = epoch; running = true; startedAt = Date.now(); reasoningText = '';
  $('reasoning').hidden = $('live-response').hidden = true;
  controls(); activity(suffix === 'commands' ? `Running ${body.command}` : suffix === 'run' ? 'Running python3 main.py' : 'Waiting for model');
  try { await api(endpoint(suffix), 'POST', body); }
  catch (error) { if (generation === epoch) { running = false; controls(); updateActivity(); $('connection').textContent = 'Request failed'; } showError(error); throw error; }
}
$('create-project').onsubmit = async event => {
  event.preventDefault(); const button = event.currentTarget.querySelector('button'); button.disabled = true;
  try { const entry = await api('/api/projects', 'POST', { name: $('project-name').value }); $('project-name').value = ''; selectProject(entry); }
  catch (error) { showError(error); } finally { button.disabled = false; }
};
$('compose').onsubmit = async event => {
  event.preventDefault(); const text = $('prompt').value;
  try { await start('messages', { text }); $('prompt').value = ''; } catch { /* start displays errors */ }
};
$('run').onclick = () => executeCommand('python3 main.py', 'run');
$('stop').onclick = () => api(endpoint('cancel'), 'POST').catch(showError);
$('command-stop').onclick = $('stop').onclick;
function renderCommandHistory() {
  const list = commandHistory.get(project.id) || [];
  $('command-history').replaceChildren();
  for (const command of list) { const option = document.createElement('option'); option.value = command; $('command-history').append(option); }
  $('cli-command').value = list[0] || 'python3 main.py --help';
}
function renderCommandResult(f) {
  const result = f.result || {};
  let status = result.timed_out ? 'Timed out' : `Exit ${result.exit_code ?? '?'}`;
  if (f.text === 'Stopped.') status = 'Stopped';
  else if (f.kind === 'turn_error') status = f.text;
  $('command-status').textContent = status;
  $('command-status').classList.toggle('failed', f.kind === 'turn_error' || result.timed_out || result.exit_code !== 0);
  const stdout = result.stdout || '', stderr = result.stderr || '';
  $('output').textContent = `$ ${f.command}\n${stdout}${stderr ? `${stdout && !stdout.endsWith('\n') ? '\n' : ''}[stderr]\n${stderr}` : ''}\n[${status}]`;
}
async function executeCommand(command, suffix = 'commands') {
  if (!command.trim()) return;
  const generation = epoch, id = project.id;
  $('command-status').textContent = 'Starting…'; $('command-status').classList.remove('failed');
  $('output').textContent = `$ ${command}\nRunning…`;
  try {
    await start(suffix, suffix === 'commands' ? { command } : undefined);
    const list = commandHistory.get(id) || [];
    commandHistory.set(id, [command, ...list.filter(item => item !== command)].slice(0, 20));
    if (generation === epoch) renderCommandHistory();
  } catch { if (generation === epoch) $('command-status').textContent = 'Command could not start'; }
}
$('command-form').onsubmit = event => { event.preventDefault(); executeCommand($('cli-command').value); };
function activateTab(id) {
  document.querySelectorAll('.panel').forEach(panel => panel.classList.toggle('active', panel.id === id));
  document.querySelectorAll('[data-tab]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.tab === id)));
}
document.querySelectorAll('[data-tab]').forEach(button => { button.onclick = () => activateTab(button.dataset.tab); });
projects().then(list => {
  const previous = localStorage.getItem('coderun-project');
  const entry = list.find(p => p.id === previous) || list[0]; if (entry) selectProject(entry);
}).catch(showError);
