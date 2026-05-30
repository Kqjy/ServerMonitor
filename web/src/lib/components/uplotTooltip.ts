import type uPlot from 'uplot';
import type { Plugin } from 'uplot';
import { autoDigits } from '$lib/format';

export type TooltipFormat = (value: number) => string;

export function tooltipPlugin(opts: {
  format?: TooltipFormat;
  unit?: string;
} = {}): Plugin {
  const fmt: TooltipFormat =
    opts.format ?? ((v) => {
      if (!Number.isFinite(v)) return '—';
      const d = Math.max(2, autoDigits(v));
      return `${v.toFixed(d)}${opts.unit ? ' ' + opts.unit : ''}`;
    });

  let tip: HTMLDivElement | null = null;
  let lastIdx = -1;

  function setHidden(hidden: boolean) {
    if (!tip) return;
    tip.style.display = hidden ? 'none' : 'block';
  }

  return {
    hooks: {
      init(u: uPlot) {
        tip = document.createElement('div');
        tip.style.position = 'absolute';
        tip.style.pointerEvents = 'none';
        tip.style.zIndex = '5';
        tip.style.display = 'none';
        tip.style.minWidth = '120px';
        tip.style.padding = '6px 8px';
        tip.style.borderRadius = '6px';
        tip.style.border = '1px solid oklch(0.27 0 0)';
        tip.style.background = 'oklch(0.17 0 0 / 0.95)';
        tip.style.color = 'oklch(0.92 0 0)';
        tip.style.font = '11px ui-monospace, SFMono-Regular, Menlo, monospace';
        tip.style.lineHeight = '1.4';
        tip.style.boxShadow = '0 4px 12px oklch(0 0 0 / 0.4)';
        tip.style.whiteSpace = 'nowrap';
        u.over.appendChild(tip);
      },
      destroy() {
        tip?.remove();
        tip = null;
      },
      setCursor(u: uPlot) {
        if (!tip) return;
        const { idx, left, top } = u.cursor;
        if (idx == null || left == null || top == null || left < 0 || top < 0) {
          setHidden(true);
          lastIdx = -1;
          return;
        }
        const xs = u.data[0] as number[];
        const ts = xs[idx];
        if (ts == null) {
          setHidden(true);
          lastIdx = -1;
          return;
        }
        if (pointerBeyondData(u, xs, idx, left, ts)) {
          setHidden(true);
          lastIdx = -1;
          return;
        }
        if (idx === lastIdx && tip.style.display === 'block') {
          positionTip(tip, u.over, left, top);
          return;
        }
        lastIdx = idx;

        const date = new Date(ts * 1000);
        const timeStr = formatTimestamp(date);

        const rows: string[] = [];
        rows.push(`<div style="color: oklch(0.64 0 0); margin-bottom: 4px">${timeStr}</div>`);

        let any = false;
        for (let i = 1; i < u.series.length; i++) {
          const s = u.series[i];
          if (s.show === false) continue;
          const v = (u.data[i] as Array<number | null | undefined>)[idx];
          if (v == null || !Number.isFinite(v as number)) continue;
          any = true;
          const stroke = typeof s.stroke === 'function' ? s.stroke(u, i) : s.stroke;
          const color = typeof stroke === 'string' ? stroke : 'oklch(0.78 0.16 162)';
          const showLabel = u.series.length > 2 && s.label && s.label !== 'value';
          rows.push(
            `<div style="display: flex; align-items: center; gap: 6px; justify-content: space-between">` +
              `<span style="display: inline-flex; align-items: center; gap: 6px">` +
                `<span style="display: inline-block; height: 6px; width: 6px; border-radius: 999px; background: ${color}"></span>` +
                `${showLabel ? `<span style="color: oklch(0.78 0 0)">${escapeHtml(String(s.label))}</span>` : ''}` +
              `</span>` +
              `<span style="color: oklch(0.95 0 0)">${escapeHtml(fmt(v as number))}</span>` +
            `</div>`
          );
        }

        if (!any) {
          setHidden(true);
          return;
        }

        tip.innerHTML = rows.join('');
        setHidden(false);
        positionTip(tip, u.over, left, top);
      }
    }
  };

  function positionTip(el: HTMLDivElement, over: HTMLElement, left: number, top: number) {
    const rect = el.getBoundingClientRect();
    const w = rect.width || 140;
    const h = rect.height || 40;
    const overWidth = over.clientWidth;
    const overHeight = over.clientHeight;
    const offset = 12;
    let x = left + offset;
    let y = top + offset;
    if (x + w > overWidth) x = left - w - offset;
    if (y + h > overHeight) y = top - h - offset;
    if (x < 0) x = 0;
    if (y < 0) y = 0;
    el.style.left = `${x}px`;
    el.style.top = `${y}px`;
  }
}

function pointerBeyondData(u: uPlot, xs: number[], idx: number, left: number, ts: number): boolean {
  const n = xs.length;
  const atLeft = idx === 0;
  const atRight = idx === n - 1;
  if (!atLeft && !atRight) return false;
  const ptLeft = u.valToPos(ts, 'x');
  const beyond = atLeft && atRight ? true : atLeft ? left < ptLeft : left > ptLeft;
  if (!beyond) return false;
  let halfStepPx = 16;
  if (n >= 2) {
    const adj = atLeft ? xs[1] : xs[n - 2];
    halfStepPx = Math.abs(u.valToPos(adj, 'x') - ptLeft) / 2;
  }
  return Math.abs(left - ptLeft) > halfStepPx;
}

function formatTimestamp(d: Date): string {
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  const today = new Date();
  if (d.toDateString() === today.toDateString()) return `${hh}:${mm}:${ss}`;
  const mo = String(d.getMonth() + 1).padStart(2, '0');
  const dy = String(d.getDate()).padStart(2, '0');
  return `${mo}-${dy} ${hh}:${mm}:${ss}`;
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    c === '&' ? '&amp;' : c === '<' ? '&lt;' : c === '>' ? '&gt;' : c === '"' ? '&quot;' : '&#39;'
  );
}
