import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/vazirmatn';
import './style.css';
import { APIError, request, type Audit, type Backend, type BackendStatus, type Config, type Point, type Session, type Status } from './api';
import { clockFormat, fmt, numberFormat, percent, strategies, timestamp } from './display';
import { RouteTopology, StatusBadge } from './Topology';

const mbps = (value?: number) => value === undefined ? '—' : fmt(value * 8 / 1e6);
const traffic = (value?: number) => value === undefined ? '—' : fmt(value / 1e9);
const kinds: Record<string, string> = { healthy: 'بازیابی مسیر', unhealthy: 'خرابی مسیر', started: 'راه‌اندازی موتور', configuration: 'تغییر پیکربندی', 'dial-failover': 'تغییر مسیر اتصال', 'manual-check': 'آزمون دستی', login: 'ورود مدیر', 'admin-created': 'ایجاد حساب', 'admin-removed': 'حذف حساب', 'password-changed': 'تغییر رمز' };
const ranges: Record<string, string> = { '5m': '5 دقیقه', '15m': '15 دقیقه', '1h': '1 ساعت', '24h': '24 ساعت', '7d': '7 روز' };
const blankBackend: Backend = { id: '', name: '', tcp: '', udp: '', probe: '', weight: 1, capacity: 0, rateMbps: 0, enabled: true, drain: false };
const errorText = (error: unknown) => error instanceof Error ? error.message : 'خطای پردازش درخواست';

function Theme({ light, setLight }: { light: boolean; setLight: (value: boolean) => void }) {
  return <button className="quiet" onClick={() => setLight(!light)}>{light ? 'نمای تیره' : 'نمای روشن'}</button>;
}

function Login({ onLogin }: { onLogin: (session: Session) => void }) {
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    setBusy(true);
    const data = new FormData(event.currentTarget);
    try { onLogin(await request<Session>('/login', 'POST', Object.fromEntries(data))); }
    catch (error) { setError(error instanceof APIError && error.status === 401 ? 'نام کاربری یا رمز عبور نادرست است.' : errorText(error)); }
    finally { setBusy(false); }
  }
  return <main className="login"><div className="brand"><span className="logo">B</span><span dir="ltr">BackPack Smart Router</span></div>
    <form className="card login-form" onSubmit={submit}><h1>ورود به پنل</h1>
      <label>نام کاربری<input name="name" dir="ltr" autoComplete="username" required /></label>
      <label>رمز عبور<input type="password" name="password" dir="ltr" autoComplete="current-password" required /></label>
      {error && <p className="notice error" role="alert">{error}</p>}
      <button className="primary" disabled={busy}>{busy ? 'در حال ورود…' : 'ورود'}</button>
    </form></main>;
}

function Chart({ points, label, light }: { points: Point[]; label: string; light: boolean }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const canvas = ref.current;
    if (!canvas || points.length < 2) return;
    const draw = () => {
      const width = canvas.clientWidth, height = canvas.clientHeight, ratio = window.devicePixelRatio || 1;
      canvas.width = width * ratio; canvas.height = height * ratio;
      const context = canvas.getContext('2d');
      if (!context) return;
      context.scale(ratio, ratio);
      const css = getComputedStyle(canvas);
      const left = 49, right = width - 12, top = 14, bottom = height - 32;
      const maxValue = Math.max(1, ...points.map(point => Math.max(point.rx, point.tx) * 8 / 1e6));
      const start = points[0].time, elapsed = Math.max(1, points.at(-1)!.time - start);
      context.font = '11px Vazirmatn Variable, sans-serif';
      context.direction = 'ltr';
      context.textAlign = 'left';
      context.fillStyle = css.getPropertyValue('--muted');
      context.strokeStyle = css.getPropertyValue('--line');
      for (let tick = 0; tick <= 4; tick++) {
        const y = top + (bottom - top) * tick / 4;
        context.beginPath(); context.moveTo(left, y); context.lineTo(right, y); context.stroke();
        context.fillText(numberFormat.format(maxValue * (1 - tick / 4)), 2, y + 3);
      }
      context.fillText(clockFormat.format(start * 1000), left, height - 6);
      context.textAlign = 'right';
      context.fillText(clockFormat.format((start + elapsed) * 1000), right, height - 6);
      const gaps = points.slice(1).map((point, index) => point.time - points[index].time).sort((a, b) => a - b);
      const maxGap = Math.max(12, gaps[Math.floor(gaps.length / 2)] * 3);
      for (const [key, color] of [['rx', '--accent'], ['tx', '--secondary']] as const) {
        context.strokeStyle = css.getPropertyValue(color);
        context.lineWidth = 2;
        context.beginPath();
        points.forEach((point, index) => {
          const x = left + (right - left) * (point.time - start) / elapsed;
          const y = bottom - (bottom - top) * (point[key] * 8 / 1e6) / maxValue;
          if (index === 0 || point.time - points[index - 1].time > maxGap) context.moveTo(x, y);
          else context.lineTo(x, y);
        });
        context.stroke();
      }
    };
    const observer = new ResizeObserver(draw);
    observer.observe(canvas);
    draw();
    return () => observer.disconnect();
  }, [points, light]);
  return points.length > 1 ? <canvas ref={ref} aria-label={label} role="img" />
    : <div className="empty">برای این بازه هنوز نمونهٔ کافی جمع نشده است.</div>;
}

function Overview({ status, cfg, light, openBackend }: {
  status: Status | null; cfg: Config | null; light: boolean; openBackend: (group: string, id: string) => void;
}) {
  const [range, setRange] = useState('5m');
  const [points, setPoints] = useState<Point[]>([]);
  const [error, setError] = useState('');
  useEffect(() => {
    let current = true;
    setPoints([]);
    const refresh = () => request<Point[]>('/metrics?range=' + range)
      .then(values => { if (current) { setPoints(values); setError(''); } })
      .catch(error => { if (current) setError(errorText(error)); });
    void refresh();
    const timer = window.setInterval(refresh, 10_000);
    return () => { current = false; clearInterval(timer); };
  }, [range]);
  const backends = status?.backends ?? [];
  const stats = [
    { title: 'ورودی سالم', value: status?.online ? fmt(backends.filter(b => b.state === 'healthy').length) + ' / ' + fmt(backends.length) : '—', unit: 'مسیر' },
    { title: status?.kernelTraffic ? 'جریان فعال در Go' : 'اتصال و جریان فعال', value: fmt(status?.online ? status.active : undefined), unit: 'جریان' },
    { title: 'دریافت / ارسال', value: status?.online ? mbps(status.rxRate) + ' / ' + mbps(status.txRate) : '—', unit: 'Mbps' },
    { title: 'ترافیک عبوری', value: traffic(status?.online ? status.rx + status.tx : undefined), unit: 'GB' },
  ];
  return <>
    {status?.kernelTraffic === 'unavailable' && <p className="notice" role="status">آمار مسیر کرنل فعلاً در دسترس نیست. اعداد ترافیک فقط مسیر Go را نشان می‌دهند.</p>}
    <section className="stats">{stats.map(stat => <article className="card stat" key={stat.title}><h2>{stat.title}</h2>
      <div><strong dir="ltr">{stat.value}</strong> <small>{stat.unit}</small></div></article>)}</section>
    <RouteTopology status={status} config={cfg} light={light} openBackend={openBackend} />
    <section className="card chart-card"><div className="section-title"><div><h2>ترافیک تجمیعی</h2>
      <div className="legend"><span className="rx">دریافت</span><span className="tx">ارسال</span><span>مگابیت بر ثانیه</span></div></div>
      <select aria-label="بازهٔ نمودار" value={range} onChange={event => setRange(event.target.value)}>
        {Object.entries(ranges).map(([value,label]) => <option key={value} value={value}>{label}</option>)}
      </select></div>
      {error ? <p role="alert">{error}</p> : <Chart points={points} label="نمودار نرخ دریافت و ارسال" light={light} />}
    </section>
    {status?.decision && <section className="card decision"><h2>آخرین انتخاب مسیر</h2>
      <p>{backends.find(b => b.id === status.decision?.backend)?.name ?? status.decision.backend} · {strategies[status.decision.strategy]} · امتیاز <bdi dir="ltr">{fmt(status.decision.score)}</bdi>
        {status.decision.retry && ' · انتخاب دوباره پس از خطای اتصال'}</p></section>}
  </>;
}

type Save = (config: Config) => Promise<boolean>;
type BackendFocus = { group: string; id: string };

function Backends({ cfg, status, csrf, save, focus }: {
  cfg: Config; status: Status | null; csrf: string; save: Save; focus: BackendFocus | null;
}) {
  const [group, setGroup] = useState(focus?.group ?? cfg.groups.find(item => item.backends?.length)?.id ?? cfg.groups[0].id);
  const [edit, setEdit] = useState<{ previous?: string; row: Backend } | null>(() => {
    const row = cfg.groups.find(item => item.id === focus?.group)?.backends?.find(item => item.id === focus?.id);
    return row ? { previous: row.id, row: { ...row } } : null;
  });
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const selected = cfg.groups.find(item => item.id === group) ?? cfg.groups[0];
  const live = new Map((status?.backends ?? []).map(item => [item.id, item]));
  function update(rows: Backend[]) {
    return { ...cfg, groups: cfg.groups.map(item => item.id === selected.id ? { ...item, backends: rows } : item) };
  }
  async function apply(next: Config) {
    setBusy(true);
    try { return await save(next); } finally { setBusy(false); }
  }
  async function check(id: string) {
    setMessage(''); setBusy(true);
    try { await request('/backends/' + encodeURIComponent(id) + '/check', 'POST', undefined, csrf); setMessage('آزمون سلامت موفق بود.'); }
    catch (error) { setMessage(errorText(error)); }
    finally { setBusy(false); }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!edit) return;
    const rows = edit.previous ? selected.backends.map(row => row.id === edit.previous ? edit.row : row) : [...selected.backends, edit.row];
    if (await apply(update(rows))) setEdit(null);
  }
  const fields = [['id','شناسه'],['name','نام مسیر'],['tcp','ورودی خصوصی TCP'],['udp','ورودی خصوصی UDP'],['probe','آدرس عامل سلامت'],['weight','وزن'],['capacity','سقف جریان (0 یعنی بدون سقف)'],['rateMbps','ظرفیت لینک (Mbps)']] as const;
  return <section className="card"><div className="section-title"><div><h2>ورودی‌های خصوصی</h2>
    <p className="muted">اتصال‌های باز هنگام تخلیه در مسیر فعلی ادامه پیدا می‌کنند.</p></div>
    <div className="actions"><select aria-label="گروه ورودی" value={selected.id} onChange={event => { setGroup(event.target.value); setEdit(null); }}>
      {cfg.groups.map(item => <option key={item.id} value={item.id}>
        {[item.id, item.tcp && `TCP ${item.tcp.split(':').at(-1)}`, item.udp && `UDP ${item.udp.split(':').at(-1)}`].filter(Boolean).join(' · ')}
      </option>)}</select>
      <button className="primary" onClick={() => setEdit({ row: { ...blankBackend } })}>افزودن ورودی</button></div></div>
    {message && <p className="notice" role="status">{message}</p>}
    {edit && <form className="editor" onSubmit={submit}><h3>{edit.previous ? 'ویرایش ورودی' : 'ورودی جدید'}</h3><div className="form-grid">
      {fields.map(([key,label]) => <label key={key}>{label}<input value={edit.row[key]} dir={key === 'name' ? 'auto' : 'ltr'}
        type={typeof edit.row[key] === 'number' ? 'number' : 'text'} readOnly={key === 'id' && !!edit.previous}
        required={['id','name','weight','capacity','rateMbps'].includes(key)}
        onChange={event => setEdit({ ...edit, row: { ...edit.row, [key]: typeof edit.row[key] === 'number' ? Number(event.target.value) : event.target.value } })} /></label>)}
      </div><label className="checkbox"><input type="checkbox" checked={edit.row.enabled}
        onChange={event => setEdit({ ...edit, row: { ...edit.row, enabled: event.target.checked } })} /> فعال</label>
      <div className="actions"><button className="primary" disabled={busy}>ذخیرهٔ ورودی</button><button type="button" onClick={() => setEdit(null)}>بستن</button></div>
    </form>}
    <div className="table-wrap"><table><thead><tr>{['نام و مقصد','وضعیت',status?.kernelTraffic ? 'TCP / UDP در Go' : 'اتصال / UDP','دریافت / ارسال','تأخیر','عملیات'].map(label => <th key={label}>{label}</th>)}</tr></thead>
      <tbody>{selected.backends.map(row => {
        const b = live.get(row.id);
        return <tr key={row.id}><td><strong>{row.name}</strong><small dir="ltr">{row.tcp || row.udp}</small></td>
          <td><StatusBadge state={b?.state} /><small>{row.probe ? 'آزمون سرتاسری' : 'آزمون پورت'}</small></td>
          <td><bdi dir="ltr">{fmt(b?.tcpActive)} / {fmt(b?.udpActive)}</bdi></td><td><bdi dir="ltr">{mbps(b?.rxRate)} / {mbps(b?.txRate)} Mbps</bdi></td>
          <td><bdi dir="ltr">{fmt(b?.rtt || null)} ms</bdi></td>
          <td><div className="actions"><button onClick={() => setEdit({ previous: row.id, row: { ...row } })}>ویرایش</button>
            <button disabled={busy || !row.enabled} onClick={() => void check(row.id)}>آزمون</button>
            <button disabled={busy || !row.enabled} onClick={() => void apply(update(selected.backends.map(b => b.id === row.id ? { ...b, drain: !b.drain } : b)))}>{row.drain ? 'بازگردانی' : 'تخلیه'}</button>
            <button className="danger" disabled={busy} onClick={() => { if (confirm('این ورودی حذف شود؟')) void apply(update(selected.backends.filter(b => b.id !== row.id))); }}>حذف</button></div></td>
        </tr>;
      })}</tbody></table></div>
    {!selected.backends.length && <p className="empty">برای این گروه ورودی تعریف نشده است.</p>}
    <div className="telemetry">{selected.backends.filter(row => live.has(row.id)).map(row => <Telemetry key={row.id} b={live.get(row.id)!} />)}</div>
  </section>;
}

function Telemetry({ b }: { b: BackendStatus }) {
  return <details><summary>{b.name} · اطلاعات VM</summary>
    <dl>
      <dt>امتیاز سلامت</dt><dd><bdi dir="ltr">{fmt(b.healthScore)}</bdi></dd>
      <dt>پردازنده / حافظه</dt><dd><bdi dir="ltr">{percent(b.cpu)} / {percent(b.ram)}</bdi></dd>
      <dt>ارسال مجدد TCP</dt><dd><bdi dir="ltr">{percent(b.retransmits)}</bdi></dd>
      <dt>افت بسته</dt><dd><bdi dir="ltr">{percent(b.loss)}</bdi></dd>
      <dt>دریافت / ارسال رابط VM</dt><dd><bdi dir="ltr">{traffic(b.interfaceRx ?? undefined)} / {traffic(b.interfaceTx ?? undefined)} GB</bdi></dd>
      <dt>آخرین آزمون موفق</dt><dd><bdi dir="ltr">{b.lastOK.startsWith('0001') ? '—' : timestamp(b.lastOK)}</bdi></dd>
      <dt>آخرین خطا</dt><dd dir="auto">{b.failure || '—'}</dd>
    </dl></details>;
}

function Routing({ cfg, save }: { cfg: Config; save: Save }) {
  const [draft, setDraft] = useState(cfg);
  const [busy, setBusy] = useState(false);
  useEffect(() => setDraft(cfg), [cfg]);
  const options = [['maxFlows','سقف جریان هم‌زمان (0 یعنی بدون سقف)'],['dialTimeoutMS','مهلت اتصال (ms)'],['tcpIdleSeconds','انقضای TCP (ثانیه)'],
    ['udpIdleSeconds','انقضای UDP (ثانیه)'],['checkIntervalMS','فاصلهٔ آزمون (ms)'],['checkTimeoutMS','مهلت آزمون (ms)'],
    ['failures','آستانهٔ خرابی'],['recoveries','آستانهٔ بازیابی'],['cooldownSeconds','انتظار بازیابی (ثانیه)']] as const;
  const weights = [['flows','جریان',4],['rate','ترافیک',3],['rtt','تأخیر',2],['cpu','پردازنده',2],['ram','حافظه',1],['retransmits','ارسال مجدد',1],['loss','افت بسته',2]] as const;
  return <form className="card" onSubmit={event => { event.preventDefault(); setBusy(true); void save(draft).finally(() => setBusy(false)); }}>
    <h2>سیاست انتخاب مسیر</h2><p className="muted">بعد از اتصال مجدد، IP خروجی ممکن است تغییر کند.</p>
    <label>راهبرد<select value={draft.strategy} onChange={event => setDraft({ ...draft, strategy: event.target.value })}>
      {Object.entries(strategies).map(([key,label]) => <option key={key} value={key}>{label}</option>)}</select></label>
    <div className="form-grid">{options.map(([key,label]) => <label key={key}>{label}<input dir="ltr" required type="number" value={draft[key]}
      onChange={event => setDraft({ ...draft, [key]: Number(event.target.value) })} /></label>)}</div>
    {draft.strategy === 'adaptive' && <><h3>وزن شاخص‌های تطبیقی</h3><div className="form-grid">
      {weights.map(([key,label,defaultValue]) => <label key={key}>{label}<input type="number" dir="ltr" min="0" max="100" step="0.1"
        value={draft.weights[key] ?? defaultValue} onChange={event => setDraft({ ...draft, weights: { ...draft.weights, [key]: Number(event.target.value) } })} /></label>)}
    </div></>}
    <button className="primary" disabled={busy}>اعمال سیاست</button>
  </form>;
}

function Events({ cfg }: { cfg: Config | null }) {
  const [kind, setKind] = useState('');
  const [backend, setBackend] = useState('');
  const [hours, setHours] = useState('24');
  const [rows, setRows] = useState<Audit[]>([]);
  const [error, setError] = useState('');
  useEffect(() => {
    let current = true;
    const refresh = () => request<Audit[]>('/events?' + new URLSearchParams({ kind, backend, hours }))
      .then(items => { if (current) { setRows(items); setError(''); } })
      .catch(error => { if (current) setError(errorText(error)); });
    void refresh();
    const timer = window.setInterval(refresh, 7000);
    return () => { current = false; clearInterval(timer); };
  }, [kind, backend, hours]);
  return <section className="card"><h2>رویدادها و تغییرات</h2><div className="actions filters">
    <select aria-label="نوع رویداد" value={kind} onChange={event => setKind(event.target.value)}>
      <option value="">همهٔ رویدادها</option>{Object.entries(kinds).map(([key,label]) => <option key={key} value={key}>{label}</option>)}</select>
    <select aria-label="فیلتر ورودی" value={backend} onChange={event => setBackend(event.target.value)}>
      <option value="">همهٔ ورودی‌ها</option>{cfg?.groups.flatMap(group => group.backends.map(b => <option key={b.id} value={b.id}>{b.name}</option>))}</select>
    <select aria-label="بازهٔ رویداد" value={hours} onChange={event => setHours(event.target.value)}>
      <option value="1">1 ساعت</option><option value="24">24 ساعت</option><option value="168">7 روز</option></select></div>
    {error && <p role="alert">{error}</p>}
    {rows.length ? <div className="table-wrap"><table><thead><tr>{['زمان','رویداد','ورودی','جزئیات'].map(label => <th key={label}>{label}</th>)}</tr></thead>
      <tbody>{rows.map(row => <tr key={row.id}><td><bdi dir="ltr">{timestamp(row.time * 1000)}</bdi></td><td>{kinds[row.kind] ?? row.kind}</td>
        <td>{row.backend || '—'}</td><td dir="auto">{row.detail}<small dir="ltr">{row.actor.startsWith('engine:') ? 'engine' : row.actor}</small></td></tr>)}</tbody></table></div>
      : <p className="empty">رویدادی برای این فیلتر ثبت نشده است.</p>}
  </section>;
}

function Settings({ cfg, session, save, signOut }: { cfg: Config | null; session: Session; save: Save; signOut: () => void }) {
  const [admins, setAdmins] = useState<string[]>([]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const refresh = useCallback(() => request<string[]>('/admins').then(setAdmins).catch(error => setError(errorText(error))), []);
  useEffect(() => { void refresh(); }, [refresh]);
  async function manage(operation: () => Promise<unknown>) {
    setBusy(true); setError('');
    try { await operation(); } catch (error) { setError(errorText(error)); }
    finally { setBusy(false); }
  }
  function download() {
    const blob = new Blob([JSON.stringify(cfg, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url; link.download = 'backpack-router-config.json'; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 500);
  }
  return <section className="card"><h2>تنظیمات پنل</h2>
    {error && <p className="notice error" role="alert">{error}</p>}
    <h3>حساب‌های مدیر</h3><ul className="admins">{admins.map(name => <li key={name}><bdi>{name}</bdi>
      {name !== session.name && <button className="danger" disabled={busy} onClick={() => { if (confirm('این حساب حذف شود؟')) void manage(async () => {
        await request('/admins/' + encodeURIComponent(name), 'DELETE', undefined, session.csrf); await refresh();
      }); }}>حذف</button>}</li>)}</ul>
    <form className="form-grid" onSubmit={event => {
      event.preventDefault(); const form = event.currentTarget; const data = Object.fromEntries(new FormData(form));
      void manage(async () => { await request('/admins','POST',data,session.csrf); form.reset(); await refresh(); });
    }}><label>نام مدیر جدید<input dir="ltr" name="name" autoComplete="off" required /></label>
      <label>رمز مدیر جدید<input name="password" type="password" minLength={14} autoComplete="new-password" required /></label>
      <button disabled={busy}>افزودن مدیر</button></form>
    <h3>تغییر رمز حساب</h3><form className="form-grid" onSubmit={event => {
      event.preventDefault(); const data = Object.fromEntries(new FormData(event.currentTarget));
      void manage(async () => { await request('/password','PUT',data,session.csrf); signOut(); });
    }}><label>رمز فعلی<input name="current" type="password" autoComplete="current-password" required /></label>
      <label>رمز جدید<input name="password" type="password" minLength={14} autoComplete="new-password" required /></label>
      <button disabled={busy}>تغییر رمز</button></form>
    <h3>پشتیبان پیکربندی</h3><div className="actions"><button disabled={!cfg || busy} onClick={download}>دریافت فایل پیکربندی</button>
      <label className="file">بازیابی فایل<input type="file" accept=".json,application/json" disabled={!cfg || busy} onChange={event => {
        const file = event.target.files?.[0]; event.target.value = '';
        if (!file || !cfg || !confirm('پیکربندی فایل اعمال شود؟')) return;
        void manage(async () => {
          if (file.size > 256 * 1024) throw new Error('حجم فایل بیش از حد مجاز است.');
          const imported = JSON.parse(await file.text()) as Config;
          await save({ ...imported, revision: cfg.revision });
        });
      }} /></label></div>
  </section>;
}

const tabs = ['نمای کلی','ورودی‌ها','مسیر‌یابی','رویدادها','تنظیمات'] as const;
type Tab = typeof tabs[number];

function Dashboard({ session, signOut, light, setLight }: { session: Session; signOut: () => void; light: boolean; setLight: (state: boolean) => void }) {
  const [tab, setTab] = useState<Tab>('نمای کلی');
  const [backendFocus, setBackendFocus] = useState<BackendFocus | null>(null);
  const [status, setStatus] = useState<Status | null>(null);
  const [cfg, setCfg] = useState<Config | null>(null);
  const [notice, setNotice] = useState('');
  const [connected, setConnected] = useState(false);
  useEffect(() => {
    let current = true;
    const stream = new EventSource('/api/v1/live');
    stream.onopen = () => setConnected(true);
    stream.onmessage = event => setStatus(JSON.parse(event.data));
    stream.onerror = () => {
      setConnected(false);
      void request<Session>('/session').catch(error => {
        if (current && error instanceof APIError && error.status === 401) signOut();
      });
    };
    void request<Config>('/config').then(value => { if (current) setCfg(value); }).catch(error => { if (current) setNotice(errorText(error)); });
    void request<Status>('/status').then(value => { if (current) setStatus(previous => previous ?? value); }).catch(() => {});
    return () => { current = false; stream.close(); };
  }, [signOut]);
  async function save(config: Config) {
    try { setCfg(await request<Config>('/config','PUT',config,session.csrf)); setNotice('پیکربندی ذخیره شد.'); return true; }
    catch (error) {
      if (error instanceof APIError && error.status === 409) {
        try { setCfg(await request<Config>('/config')); } catch { /* Display the original conflict. */ }
      }
      setNotice(errorText(error)); return false;
    }
  }
  async function logout() {
    try { await request('/logout','POST',undefined,session.csrf); signOut(); }
    catch (error) { if (error instanceof APIError && error.status === 401) signOut(); else setNotice(errorText(error)); }
  }
  return <div className="shell"><aside className="sidebar">
    <div className="brand"><span className="logo">B</span><span dir="ltr">BackPack<small>SMART ROUTER</small></span></div>
    <nav aria-label="بخش‌های پنل">{tabs.map(name => <button key={name} aria-current={tab === name ? 'page' : undefined} onClick={() => { setBackendFocus(null); setTab(name); }}>{name}</button>)}</nav>
    <div className="account"><bdi>{session.name}</bdi><button className="quiet" onClick={() => void logout()}>خروج</button></div>
    </aside><main className="dashboard"><header className="topbar"><div><h1>{tab}</h1>
      <span className={'connection ' + (connected && status?.online ? 'on' : '')}>
        {connected && status?.online ? 'دادهٔ زنده' : status?.online === false ? 'موتور در دسترس نیست' : 'در انتظار اتصال زنده'}</span></div>
      <Theme light={light} setLight={setLight} /></header>
    {notice && <div className="notice" role="status">{notice}<button className="quiet" onClick={() => setNotice('')}>بستن</button></div>}
    {cfg && status && cfg.revision > status.revision && <p className="notice">نسخهٔ جدید ذخیره شده و در انتظار اعمال روی موتور است.</p>}
    {tab === 'نمای کلی' && <Overview status={connected ? status : null} cfg={cfg} light={light}
      openBackend={(group, id) => { setBackendFocus({ group, id }); setTab('ورودی‌ها'); }} />}
    {tab === 'ورودی‌ها' && (cfg ? <Backends cfg={cfg} status={status} csrf={session.csrf} save={save} focus={backendFocus} /> : <p>پیکربندی در دسترس نیست.</p>)}
    {tab === 'مسیر‌یابی' && (cfg ? <Routing cfg={cfg} save={save} /> : <p>پیکربندی در دسترس نیست.</p>)}
    {tab === 'رویدادها' && <Events cfg={cfg} />}
    {tab === 'تنظیمات' && <Settings cfg={cfg} session={session} save={save} signOut={signOut} />}
    </main></div>;
}

function App() {
  const [session, setSession] = useState<Session | null | undefined>();
  const [light, setLight] = useState(() => localStorage.getItem('backpack-theme') === 'light');
  const signOut = useCallback(() => setSession(null), []);
  useEffect(() => { void request<Session>('/session').then(setSession).catch(signOut); }, [signOut]);
  useEffect(() => { document.documentElement.dataset.theme = light ? 'light' : 'dark'; localStorage.setItem('backpack-theme',light ? 'light' : 'dark'); }, [light]);
  if (session === undefined) return <main className="login" role="status">در حال بررسی نشست…</main>;
  return session ? <Dashboard session={session} signOut={signOut} light={light} setLight={setLight} /> : <Login onLogin={setSession} />;
}

createRoot(document.getElementById('root')!).render(<App />);
