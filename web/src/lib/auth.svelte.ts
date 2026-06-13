import { api, ApiError, type Me } from '$lib/api';
import { goto } from '$app/navigation';

type Status = 'unknown' | 'needs-setup' | 'guest' | 'authed';

class AuthState {
  user = $state<Me | null>(null);
  status = $state<Status>('unknown');
  serverVersion = $state<string>('');

  async refresh(): Promise<Status> {
    try {
      const s = await api.authStatus();
      if (s.version) this.serverVersion = s.version;
      if (!s.initialized) {
        this.user = null;
        this.status = 'needs-setup';
        return this.status;
      }
    } catch {
      if (this.status === 'unknown') this.status = 'guest';
      return this.status;
    }
    try {
      const me = await api.me();
      this.user = me;
      this.status = 'authed';
    } catch (e) {
      if ((e instanceof ApiError && (e.status === 401 || e.status === 403)) || this.status === 'unknown') {
        this.user = null;
        this.status = 'guest';
      }
    }
    return this.status;
  }

  async login(username: string, password: string) {
    const me = await api.login(username, password);
    this.user = me;
    this.status = 'authed';
  }

  async setup(username: string, password: string) {
    const me = await api.setup(username, password);
    this.user = me;
    this.status = 'authed';
  }

  async logout() {
    try {
      await api.logout();
    } catch {}
    this.user = null;
    this.status = 'guest';
    await goto('/login');
  }
}

export const auth = new AuthState();
