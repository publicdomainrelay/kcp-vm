import { completeLogin, startLogin, isLocalhost } from '../lib/atproto-oauth.js';
import { clearSession, depositSession, loadSession, saveSession } from '../lib/pds.js';
import { OAUTH_SCOPE } from '../generated/oauth-scope.js';
import './swc-cluster-list.js';

const RETURN_KEY = 'swc-return-to';
const SOURCE_URL = 'https://github.com/johnandersen777/socialweb-computer-kcp';

export class SwcApp extends HTMLElement {
  connectedCallback() {
    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const state = params.get('state');

    if (code && state) {
      this._finishLogin(code, state);
      return;
    }
    if (params.get('error')) {
      const session = loadSession();
      window.history.replaceState({}, '', window.location.pathname);
      if (session) return this._renderClusters(session);
      return this._renderLogin(`Sign-in failed: ${params.get('error_description') || params.get('error')}`);
    }

    const session = loadSession();
    if (session) this._renderClusters(session);
    else this._renderLogin();
  }

  _header(extra = '') {
    return `
      <header class="header-row">
        <h1>socialweb<span class="text-muted">-computer-k8s</span></h1>
        ${extra}
      </header>`;
  }

  _footer() {
    return `
      <nav class="text-center mt-3" style="font-size:13px;">
        <a href="${SOURCE_URL}" target="_blank" rel="noopener" class="text-muted">Source Code</a>
      </nav>`;
  }

  _renderLogin(error) {
    const localhost = isLocalhost();
    this.innerHTML = `
      <main class="app-shell">
        ${this._header()}
        <p class="subheader">
          Kubernetes clusters for your Atmosphere account. The control plane is always on; nodes are
          rented from the compute market as you, only while there is work to run.
        </p>
        <div class="card">
          <h2>Sign in</h2>
          <form id="login-form" class="mt-3">
            <p class="help mb-3">Enter your handle to authenticate.</p>
            <input type="text" id="handle" name="handle" placeholder="alice.example.com"
              autocomplete="username" autocapitalize="none" spellcheck="false" required>
            <button type="submit" class="btn btn-primary btn-block mt-3" id="login-submit">Sign in with the Atmosphere</button>
          </form>
          <button id="bsky-btn" class="btn btn-secondary btn-block mt-3">Sign in with Bluesky Social</button>
          <p id="login-error" class="text-danger mt-3 ${error ? '' : 'hidden'}" style="font-size:13px;">${this._esc(error || '')}</p>
          ${localhost ? '' : '<p class="help mt-3">Your PDS will ask you to approve this app.</p>'}
        </div>
        ${this._footer()}
      </main>`;

    this.querySelector('#login-form').addEventListener('submit', (event) => {
      event.preventDefault();
      this._beginLogin(this.querySelector('#handle').value.trim());
    });
    this.querySelector('#bsky-btn').addEventListener('click', () => this._beginLogin('bsky.social'));
  }

  async _beginLogin(handle) {
    const button = this.querySelector('#login-submit');
    const error = this.querySelector('#login-error');
    error.classList.add('hidden');
    if (!handle) {
      error.textContent = 'Enter your handle.';
      error.classList.remove('hidden');
      return;
    }
    button.disabled = true;
    button.textContent = 'Redirecting to your PDS...';
    sessionStorage.setItem(RETURN_KEY, JSON.stringify({ at: Date.now() }));
    try {
      window.location.href = await startLogin(handle, OAUTH_SCOPE, null);
    } catch (err) {
      button.disabled = false;
      button.textContent = 'Sign in with the Atmosphere';
      error.textContent = String(err.message || err);
      error.classList.remove('hidden');
    }
  }

  async _finishLogin(code, state) {
    this.innerHTML = '<main class="app-shell"><div class="card text-center"><p class="text-muted">Completing sign-in...</p></div></main>';
    try {
      const { session } = await completeLogin(code, state);
      saveSession(session);
      await depositSession(session);
      window.history.replaceState({}, '', window.location.pathname);
      this._renderClusters(session);
    } catch (err) {
      window.history.replaceState({}, '', window.location.pathname);
      this._renderLogin(`Sign-in failed: ${err.message || err}`);
    }
  }

  _renderClusters(session) {
    this.innerHTML = `
      <main class="app-shell">
        ${this._header('<button class="btn btn-outline btn-sm" id="sign-out">Sign out</button>')}
        <p class="subheader">Signed in as <code>@${this._esc(session.handle || session.userDid)}</code></p>
        <swc-cluster-list id="clusters"></swc-cluster-list>
        ${this._footer()}
      </main>`;

    this.querySelector('#sign-out').addEventListener('click', () => {
      clearSession();
      this._renderLogin();
    });
    this.querySelector('#clusters').start(session, {
      onExpired: () => {
        clearSession();
        this._renderLogin('Your session is no longer accepted here. Sign in again.');
      },
    });
  }

  _esc(value) {
    return String(value).replace(/[&<>"']/g, (c) => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
    ));
  }
}

customElements.define('swc-app', SwcApp);
