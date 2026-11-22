export const numberFormat = new Intl.NumberFormat('en-US', { maximumFractionDigits: 1 });
export const clockFormat = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
export const fmt = (value?: number | null) => value == null ? '—' : numberFormat.format(value);
export const percent = (value?: number | null) => value == null ? '—' : `${fmt(value)}%`;
export const timestamp = (value: string | number) =>
  new Date(value).toLocaleString('fa-IR-u-nu-latn', { dateStyle: 'short', timeStyle: 'medium' });

export const states: Record<string, string> = {
  healthy: 'سالم', unhealthy: 'قطع', draining: 'در حال تخلیه',
  disabled: 'غیرفعال', checking: 'در حال بررسی', unknown: 'بدون داده',
};

export const strategies: Record<string, string> = {
  'weighted-least': 'کمترین اتصال وزنی', 'weighted-round-robin': 'چرخشی وزنی',
  'least-load': 'کمترین بار', 'least-rtt': 'کمترین تأخیر', adaptive: 'تطبیقی',
};

export function formatRate(bytesPerSecond?: number) {
  if (bytesPerSecond === undefined) return '—';
  const bits = bytesPerSecond * 8;
  if (bits >= 1e9) return `${fmt(bits / 1e9)} Gbps`;
  if (bits >= 1e6) return `${fmt(bits / 1e6)} Mbps`;
  if (bits >= 1e3) return `${fmt(bits / 1e3)} Kbps`;
  return `${fmt(bits)} bps`;
}
