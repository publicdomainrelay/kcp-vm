import {
  clusterDown,
  clusterUp,
  createCluster,
  deleteCluster,
  fetchConfig,
  fetchKubeconfig,
  listClusters,
  saveText,
} from '../lib/k8s-api.js';

const NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const SETTLING = new Set(['Initializing', 'Draining']);
const POLL_MS = 5000;
const NEVER = 'never';

function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function ttlLabel(sec) {
  const hours = sec / 3600;
  if (Number.isInteger(hours) && hours < 24) return hours === 1 ? '1 hour' : `${hours} hours`;
  const days = sec / 86400;
  if (Number.isInteger(days)) return days === 1 ? '1 day' : `${days} days`;
  return `${sec} s`;
}

function phaseChip(cluster) {
  const phase = cluster.phase || 'Unknown';
  return `<span class="chip chip-phase chip-phase-${esc(phase.toLowerCase())}">${esc(phase)}</span>`;
}

export class SwcClusterList extends HTMLElement {
  start({ onExpired } = {}) {
    this._onExpired = onExpired || (() => {});
    this._clusters = [];
    this._config = { ttlChoicesSec: [28800], credentialHelpers: [], kcpPublicHost: null };
    this._busy = new Set();
    this._error = '';
    this._render();
    this._load();
  }

  disconnectedCallback() {
    clearTimeout(this._timer);
  }

  async _load() {
    try {
      const [config, clusters] = await Promise.all([fetchConfig(), listClusters()]);
      this._config = config;
      this._clusters = clusters.sort((a, b) => a.name.localeCompare(b.name));
      this._error = '';
    } catch (err) {
      if (this._expired(err)) return;
      this._error = err.message || String(err);
    }
    this._loaded = true;
    this._render();
    this._schedule();
  }

  _schedule() {
    clearTimeout(this._timer);
    if (this._clusters.some((c) => SETTLING.has(c.phase))) {
      this._timer = setTimeout(() => this._load(), POLL_MS);
    }
  }

  _expired(err) {
    if (err && err.status === 401) {
      this._onExpired();
      return true;
    }
    return false;
  }

  async _act(name, fn) {
    this._busy.add(name);
    this._error = '';
    this._render();
    try {
      await fn();
    } catch (err) {
      if (this._expired(err)) return;
      this._error = `${name}: ${err.message || err}`;
    } finally {
      this._busy.delete(name);
    }
    await this._load();
  }

  _render() {
    const ttlOptions = (this._config.ttlChoicesSec || [])
      .map((sec) => `<option value="${sec}">${esc(ttlLabel(sec))}</option>`)
      .join('') + `<option value="${NEVER}">Never expires (until revoked)</option>`;
    const rows = this._clusters.map((c) => {
      const busy = this._busy.has(c.name);
      const running = c.running && c.phase !== 'Draining';
      return `
        <li class="cluster-row" data-name="${esc(c.name)}">
          <div class="cluster-head">
            <code class="cluster-name">${esc(c.name)}</code>
            ${phaseChip(c)}
          </div>
          <div class="actions">
            <select class="ttl" aria-label="kubeconfig lifetime" ${busy ? 'disabled' : ''}>${ttlOptions}</select>
            <button class="act-accent" data-act="kubeconfig" ${busy ? 'disabled' : ''}>Download kubeconfig</button>
            ${running
              ? `<button data-act="down" ${busy ? 'disabled' : ''}>Stop</button>`
              : `<button data-act="up" ${busy || c.phase === 'Draining' ? 'disabled' : ''}>Start</button>`}
            <button class="act-danger" data-act="delete" ${busy || c.phase === 'Draining' ? 'disabled' : ''}>Delete</button>
          </div>
        </li>`;
    }).join('');

    const helpers = (this._config.credentialHelpers || [])
      .map((h) => `<a class="btn btn-outline btn-sm" href="${esc(h.path)}" download>${esc(h.os)}/${esc(h.arch)}</a>`)
      .join(' ');

    this.innerHTML = `
      <div class="card mt-3">
        <h3>Clusters</h3>
        <p class="help mt-3">Each cluster's control plane stays up; its nodes are rented from the compute
        market as you, only while something is scheduled, and released when it goes idle.</p>
        <form id="create-form" class="create-row mt-3">
          <input type="text" id="cluster-name" placeholder="my-cluster" maxlength="63"
            autocomplete="off" autocapitalize="none" spellcheck="false" required>
          <button type="submit" class="btn btn-primary" id="create-submit">Create</button>
        </form>
        <p id="cluster-error" class="text-danger mt-3 ${this._error ? '' : 'hidden'}" style="font-size:13px;">${esc(this._error)}</p>
        ${!this._loaded
          ? '<p class="text-muted mt-3">Loading clusters...</p>'
          : this._clusters.length === 0
          ? '<p class="text-muted mt-3">No clusters yet.</p>'
          : `<ul class="cluster-list mt-3">${rows}</ul>`}
      </div>
      <div class="card mt-3">
        <h3>Use a kubeconfig</h3>
        <p class="help mt-3">A kubeconfig authenticates through <code>socialweb-credential</code>, which
        must be on your <code>PATH</code> at <code>/usr/local/bin/socialweb-credential</code>.</p>
        ${helpers ? `<div class="mt-3">${helpers}</div>` : ''}
        <div class="key-code mt-3">KUBECONFIG=./my-cluster.kubeconfig.yaml kubectl get pods -A</div>
        ${this._config.kcpPublicHost
          ? `<p class="help mt-3">Every kubeconfig points at <code>${esc(this._config.kcpPublicHost)}</code>.</p>`
          : ''}
      </div>`;

    this.querySelector('#create-form').addEventListener('submit', (event) => {
      event.preventDefault();
      const input = this.querySelector('#cluster-name');
      const name = input.value.trim().toLowerCase();
      if (!NAME_PATTERN.test(name) || name.length > 63) {
        this._error = 'Use lowercase letters, digits and hyphens, starting and ending with a letter or digit.';
        this._render();
        return;
      }
      this._act(name, () => createCluster(name));
    });

    for (const row of this.querySelectorAll('.cluster-row')) {
      const name = row.dataset.name;
      for (const button of row.querySelectorAll('button[data-act]')) {
        button.addEventListener('click', () => this._onAction(name, button.dataset.act, row));
      }
    }
  }

  _onAction(name, act, row) {
    if (act === 'kubeconfig') {
      const choice = row.querySelector('select.ttl').value;
      const noExpiry = choice === NEVER;
      const ttl = noExpiry ? undefined : Number(choice) || undefined;
      if (noExpiry && !window.confirm(
        `This kubeconfig for ${name} never expires. It works until it is revoked, the cluster is deleted, or the cluster CA expires. Anyone who holds the file has your access. Issue it?`,
      )) return;
      return this._act(name, async () => {
        const grant = await fetchKubeconfig(name, ttl, noExpiry);
        saveText(grant.filename, grant.kubeconfig);
      });
    }
    if (act === 'up') return this._act(name, () => clusterUp(name));
    if (act === 'down') return this._act(name, () => clusterDown(name));
    if (act === 'delete') {
      if (!window.confirm(`Delete cluster ${name}? Its objects and every kubeconfig for it stop working.`)) return;
      return this._act(name, () => deleteCluster(name));
    }
  }
}

customElements.define('swc-cluster-list', SwcClusterList);
