class LiveAnnouncer {
  message = $state('');
  private setTimer: ReturnType<typeof setTimeout> | null = null;
  private clearTimer: ReturnType<typeof setTimeout> | null = null;

  say(text: string) {
    if (this.setTimer) clearTimeout(this.setTimer);
    if (this.clearTimer) clearTimeout(this.clearTimer);
    this.message = '';
    this.setTimer = setTimeout(() => {
      this.message = text;
      this.clearTimer = setTimeout(() => (this.message = ''), 5_000);
    }, 60);
  }
}

export const announcer = new LiveAnnouncer();
