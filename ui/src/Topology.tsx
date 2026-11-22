import { useLayoutEffect, useRef, useState, type RefObject } from 'react';
import type { Backend, BackendStatus, Config, Status } from './api';
import { fmt, formatRate, percent, states, strategies, timestamp } from './display';
import './topology.css';

type Route = { backend: Backend; group: string; metrics?: BackendStatus };
type Signal = 'flow' | 'idle' | 'warming' | 'down' | 'quiet';
type Branch = { id: string; signal: Signal; share: number | undefined };
type Point = { x: number; y: number };

const signalLabels: Record<Signal, string> = {
  flow: 'دارای ترافیک', idle: 'سالم و بی‌ترافیک', warming: 'در حال بررسی یا تخلیه',
  down: 'قطع', quiet: 'غیرفعال یا بدون داده',
};
const publicIngress = import.meta.env.VITE_PUBLIC_INGRESS?.trim();

export function StatusBadge({ state = 'unknown' }: { state?: string }) {
  return <span className={'badge ' + state}>{states[state] ?? states.unknown}</span>;
}

function rate(b?: BackendStatus) {
  return b ? b.rxRate + b.txRate : undefined;
}

function branchFor(route: Route, total: number): Branch {
  const state = route.metrics?.state;
  const speed = rate(route.metrics);
  const signal: Signal = state === 'healthy' ? (speed ? 'flow' : 'idle')
    : state === 'unhealthy' ? 'down'
    : state === 'checking' || state === 'draining' ? 'warming' : 'quiet';
  return { id: route.backend.id, signal, share: speed !== undefined && total > 0 ? speed / total : undefined };
}

function WireCanvas({ map, branches, ingress, light }: {
  map: RefObject<HTMLDivElement | null>; branches: Branch[]; ingress: Signal; light: boolean;
}) {
  const ref = useRef<HTMLCanvasElement>(null);
  useLayoutEffect(() => {
    const canvas = ref.current, container = map.current;
    if (!canvas || !container) return;
    const source = container.querySelector('[data-map-source]');
    const router = container.querySelector('[data-map-router]');
    const endpoints = new Map([...container.querySelectorAll<HTMLElement>('[data-map-endpoint]')].map(el => [el.dataset.mapEndpoint, el]));
    if (!source || !router) return;

    const draw = () => {
      const area = container.getBoundingClientRect();
      if (!area.width || !area.height) return;
      const ratio = window.devicePixelRatio || 1;
      const width = Math.round(area.width * ratio), height = Math.round(area.height * ratio);
      if (canvas.width !== width || canvas.height !== height) {
        canvas.width = width;
        canvas.height = height;
      }
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
      ctx.clearRect(0, 0, area.width, area.height);
      ctx.lineCap = 'round';
      ctx.lineJoin = 'round';
      const style = getComputedStyle(container);
      const vertical = style.getPropertyValue('--map-axis').trim() === 'vertical';
      const bounds = (element: Element) => {
        const r = element.getBoundingClientRect();
        return {
          left: r.left - area.left, right: r.right - area.left, top: r.top - area.top, bottom: r.bottom - area.top,
          x: r.left - area.left + r.width / 2, y: r.top - area.top + r.height / 2,
        };
      };
      const paint = (signal: Signal, width: number, path: () => void) => {
        const color = style.getPropertyValue(`--wire-${signal}`).trim();
        ctx.setLineDash([]);
        if (signal === 'flow') {
          ctx.globalAlpha = .13;
          ctx.lineWidth = width + 7;
          ctx.strokeStyle = color;
          ctx.beginPath();
          path();
          ctx.stroke();
          ctx.globalAlpha = 1;
        }
        ctx.lineWidth = width;
        ctx.strokeStyle = color;
        ctx.setLineDash(signal === 'warming' ? [5, 5] : signal === 'quiet' || signal === 'down' ? [1, 5] : []);
        ctx.beginPath();
        path();
        ctx.stroke();
      };
      const curve = (start: Point, end: Point) => {
        const bend = (start.x + end.x) / 2;
        ctx.moveTo(start.x, start.y);
        ctx.bezierCurveTo(bend, start.y, bend, end.y, end.x, end.y);
      };
      const from = bounds(source), core = bounds(router);
      const targets = branches.flatMap(branch => {
        const element = endpoints.get(branch.id);
        return element ? [{ branch, target: bounds(element) }] : [];
      });
      paint(ingress, ingress === 'flow' ? 5 : 2, () => {
        if (vertical) {
          ctx.moveTo(from.x, from.bottom);
          ctx.lineTo(core.x, core.top);
        } else {
          curve({ x: from.right, y: from.y }, { x: core.left, y: core.y });
        }
      });
      const bus = 11;
      if (vertical && targets.length) {
        const elbow = core.bottom + 11;
        paint(ingress, ingress === 'flow' ? 4 : 2, () => {
          ctx.moveTo(core.x, core.bottom);
          ctx.lineTo(core.x, elbow);
          ctx.lineTo(bus, elbow);
          ctx.lineTo(bus, Math.max(...targets.map(({ target }) => target.y)));
        });
      }
      for (const { branch, target } of targets) {
        const width = 2 + 6 * Math.sqrt(branch.share ?? 0);
        paint(branch.signal, width, () => {
          if (vertical) {
            ctx.moveTo(bus, target.y);
            ctx.lineTo(target.left, target.y);
          } else {
            curve({ x: core.right, y: core.y }, { x: target.left, y: target.y });
          }
        });
      }
    };

    const observer = new ResizeObserver(draw);
    observer.observe(container);
    observer.observe(source);
    observer.observe(router);
    for (const element of endpoints.values()) observer.observe(element);
    draw();
    return () => observer.disconnect();
  }, [map, branches, ingress, light]);
  return <canvas ref={ref} className="map-wires" aria-hidden="true" />;
}

function MapNode({ route, branch, selected, onSelect }: {
  route: Route; branch: Branch; selected: boolean; onSelect: () => void;
}) {
  const { backend, metrics } = route;
  return <button type="button" className="map-node map-endpoint" data-map-endpoint={backend.id} data-signal={branch.signal}
    aria-pressed={selected} onClick={onSelect}>
    <span className="map-node-heading">
      <strong title={backend.name}>{backend.name}</strong><StatusBadge state={metrics?.state} />
    </span>
    <span className="map-node-address" dir="ltr">{backend.tcp || backend.udp}</span>
    <span className="map-node-stats">
      <span><bdi dir="ltr">{fmt(metrics?.active)}</bdi> اتصال</span>
      <bdi className="map-throughput" dir="ltr">{formatRate(rate(metrics))}</bdi>
      {branch.share !== undefined && <bdi className="map-share" dir="ltr" aria-label="سهم ترافیک مسیر">{percent(branch.share * 100)}</bdi>}
    </span>
  </button>;
}

function RouterListeners({ config }: { config: Config | null }) {
  const listeners = [...new Set(config?.groups.flatMap(group => [
    group.tcp ? `TCP :${group.tcp.split(':').at(-1)}` : '',
    group.udp ? `UDP :${group.udp.split(':').at(-1)}` : '',
  ]).filter(Boolean))];
  if (!listeners.length) return null;
  return <span className="router-listeners" aria-label="پورت‌های موتور">
    {listeners.map(port => <bdi key={port} dir="ltr">{port}</bdi>)}
  </span>;
}

export function RouteTopology({ status, config, light, openBackend }: {
  status: Status | null; config: Config | null; light: boolean; openBackend: (group: string, id: string) => void;
}) {
  const map = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<'map' | 'list'>('map');
  const [selection, setSelection] = useState<string | null>(null);
  const live = status?.online ? status : null;
  const routes: Route[] = live
    ? (live.backends ?? []).map(b => ({ backend: b, group: b.group, metrics: b }))
    : (config?.groups ?? []).flatMap(g => (g.backends ?? []).map(b => ({ backend: b, group: g.id })));
  const total = routes.reduce((sum, route) => sum + (rate(route.metrics) ?? 0), 0);
  const branches = routes.map(route => branchFor(route, total));
  const ingress: Signal = live ? (live.rxRate + live.txRate > 0 ? 'flow' : 'idle') : 'quiet';
  const selected = routes.find(route => route.backend.id === selection);
  const selectedBranch = branches.find(branch => branch.id === selection);
  function select(id: string) { setSelection(value => value === id ? null : id); }

  return <section className="card topology-card" aria-labelledby="topology-title">
    <div className="section-title topology-title">
      <div>
        <h2 id="topology-title">نقشهٔ مسیرهای ترافیک</h2>
        <p className="muted">ضخامت خط سهم ترافیک را نشان می‌دهد. برای جزئیات، یک مقصد را انتخاب کنید.</p>
      </div>
      <div className="view-switch" role="group" aria-label="نمایش مسیرها">
        <button type="button" aria-pressed={view === 'map'} onClick={() => setView('map')}>نقشه</button>
        <button type="button" aria-pressed={view === 'list'} onClick={() => setView('list')}>فهرست</button>
      </div>
    </div>
    {view === 'map' ? <div className="route-map" ref={map}>
      <WireCanvas map={map} branches={branches} ingress={ingress} light={light} />
      <div className="map-node map-source" data-map-source>
        <strong>ورودی ترافیک</strong>
        {publicIngress && <bdi className="map-node-address" dir="ltr">{publicIngress}</bdi>}
        <bdi className="source-rate" dir="ltr">{formatRate(live ? live.rxRate + live.txRate : undefined)}</bdi>
        <span className="map-node-meta"><bdi dir="ltr">{fmt(live?.active)}</bdi> اتصال و جریان فعال</span>
      </div>
      <div className="map-node map-router" data-map-router>
        <span className="router-name">موتور <bdi>Smart Router</bdi></span>
        <strong>{live ? (strategies[live.strategy] ?? live.strategy) : 'وضعیت موتور در دسترس نیست'}</strong>
        <RouterListeners config={config} />
      </div>
      <div className="map-endpoints">
        {routes.length ? routes.map((route, i) => <MapNode key={route.backend.id} route={route} branch={branches[i]}
          selected={selection === route.backend.id} onSelect={() => select(route.backend.id)} />)
          : <div className="map-node map-empty">{live ? 'مقصدی برای روتر تعریف نشده است.' : 'در انتظار اطلاعات مقصدها'}</div>}
      </div>
    </div> : routes.length ? <div className="table-wrap topology-table">
      <table><thead><tr>{['مقصد', 'وضعیت', 'اتصال فعال', 'ترافیک', 'سهم ترافیک', 'تأخیر بررسی'].map(label => <th key={label}>{label}</th>)}</tr></thead>
        <tbody>{routes.map(({ backend: b, metrics }, i) => {
          const share = branches[i].share;
          return <tr key={b.id}>
          <td><button type="button" className="route-name-button" aria-pressed={selection === b.id} onClick={() => select(b.id)}>{b.name}</button>
            <small dir="ltr">{b.tcp || b.udp}</small></td>
          <td><StatusBadge state={metrics?.state} /></td>
          <td><bdi dir="ltr">{fmt(metrics?.active)}</bdi></td>
          <td><bdi dir="ltr">{formatRate(rate(metrics))}</bdi></td>
          <td><bdi dir="ltr">{percent(share === undefined ? undefined : share * 100)}</bdi></td>
          <td>{metrics?.rtt ? <bdi dir="ltr">{fmt(metrics.rtt)} ms</bdi> : '—'}</td>
          </tr>;
        })}</tbody>
      </table>
    </div> : <div className="empty">{live ? 'مقصدی برای روتر تعریف نشده است.' : 'در انتظار اطلاعات مقصدها'}</div>}
    {view === 'map' && <ul className="map-legend" aria-label="معنی خطوط نقشه">
      {Object.entries(signalLabels).map(([signal, label]) =>
        <li key={signal} data-signal={signal}><span aria-hidden="true" />{label}</li>)}
    </ul>}
    {selected && <div className="route-inspector">
      <div className="section-title">
        <div className="inspector-title"><h3>جزئیات {selected.backend.name}</h3><StatusBadge state={selected.metrics?.state} /></div>
        <div className="actions">
          {config?.groups.some(group => group.id === selected.group && group.backends?.some(b => b.id === selected.backend.id)) &&
            <button type="button" onClick={() => openBackend(selected.group, selected.backend.id)}>مدیریت ورودی</button>}
          <button type="button" className="quiet" onClick={() => setSelection(null)}>بستن</button>
        </div>
      </div>
      <dl className="route-details">
        <div><dt>دریافت / ارسال</dt><dd><bdi dir="ltr">{formatRate(selected.metrics?.rxRate)} / {formatRate(selected.metrics?.txRate)}</bdi></dd></div>
        <div><dt>TCP / UDP فعال</dt><dd><bdi dir="ltr">{fmt(selected.metrics?.tcpActive)} / {fmt(selected.metrics?.udpActive)}</bdi></dd></div>
        <div><dt>سهم ترافیک</dt><dd><bdi dir="ltr">{percent(selectedBranch?.share == null ? undefined : selectedBranch.share * 100)}</bdi></dd></div>
        <div><dt>تأخیر {selected.metrics?.verified ? 'آزمون سرتاسری' : 'بررسی مسیر'}</dt><dd>{selected.metrics?.rtt ? <bdi dir="ltr">{fmt(selected.metrics.rtt)} ms</bdi> : '—'}</dd></div>
        <div><dt>آخرین آزمون موفق</dt><dd><bdi dir="ltr">{selected.metrics?.lastOK && !selected.metrics.lastOK.startsWith('0001') ? timestamp(selected.metrics.lastOK) : '—'}</bdi></dd></div>
      </dl>
      {selected.metrics?.failure && <p className="error" dir="auto">{selected.metrics.failure}</p>}
    </div>}
  </section>;
}
