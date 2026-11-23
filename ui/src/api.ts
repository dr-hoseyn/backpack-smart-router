export type Backend = {
  id: string; name: string; tcp: string; udp: string; probe: string;
  weight: number; capacity: number; rateMbps: number; enabled: boolean; drain: boolean;
};
export type BackendStatus = Backend & {
  group: string; state: string; healthScore: number; active: number; tcpActive: number; udpActive: number;
  rx: number; tx: number; rxRate: number; txRate: number; rtt: number; lastOK: string; failure: string;
  cpu: number | null; ram: number | null; loss: number | null; retransmits: number | null;
  interfaceRx: number | null; interfaceTx: number | null; verified: boolean;
};
export type Session = { name: string; csrf: string };
export type Config = {
  revision: number; strategy: string; maxFlows: number; dialTimeoutMS: number;
  tcpIdleSeconds: number; udpIdleSeconds: number; checkIntervalMS: number; checkTimeoutMS: number;
  failures: number; recoveries: number; cooldownSeconds: number; weights: Record<string, number>;
  groups: { id: string; tcp: string; udp: string; backends: Backend[] }[];
};
export type Status = {
  kernelTraffic?: 'included' | 'unavailable';
  kernelConnections?: boolean;
  online: boolean; at: string; revision: number; strategy: string; active: number; rejected: number;
  rx: number; tx: number; rxRate: number; txRate: number; backends: BackendStatus[] | null;
  decision: { time: string; group: string; backend: string; protocol: string; strategy: string; score: number; retry: boolean } | null;
};
export type Point = { time: number; rx: number; tx: number; active: number };
export type Audit = { id: number; time: number; actor: string; kind: string; backend: string; detail: string };

export class APIError extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}

export async function request<T>(path: string, method = 'GET', body?: unknown, csrf?: string): Promise<T> {
  const response = await fetch('/api/v1' + path, {
    method, headers: {
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
    }, body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({}));
    throw new APIError(response.status, error.error ?? 'درخواست انجام نشد');
  }
  return response.status === 204 ? undefined as T : response.json();
}
