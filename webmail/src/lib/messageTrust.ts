import type { components } from '../api/types';
import { verdictFromAuth, type AuthOutcome } from './mailTrust';

const outcomes = new Set<AuthOutcome>(['pass', 'fail', 'softfail', 'neutral', 'none', 'temperror', 'permerror', 'policy', 'unknown']);

export function trustFromStoredResults(results: components['schemas']['AuthResult'][] | undefined) {
  if (!results?.length) return verdictFromAuth(null);
  return verdictFromAuth({
    authservId: 'this server',
    checks: results.map(result => ({
      method: result.method,
      result: outcomes.has(result.result as AuthOutcome) ? result.result as AuthOutcome : 'unknown',
      identity: result.property,
      domain: result.domain,
      raw: `${result.method}=${result.result}`,
    })),
  });
}
