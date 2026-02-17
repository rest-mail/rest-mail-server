import * as api from '@/api/client';

/**
 * Each detection test receives the address and password, and returns a result.
 * Tests run sequentially — the first one to succeed wins.
 * Add new detection strategies by pushing to the `detectionTests` array.
 */
export interface DetectionResult {
  success: boolean;
  display_name?: string;
  method: string;
  error?: string;
}

export interface DetectionTest {
  /** Short identifier for this test (e.g., "restmail-api", "imap-autoconfig") */
  id: string;
  /** Human-readable label shown during testing */
  label: string;
  /** Run the detection. Return a result indicating success or failure. */
  run: (address: string, password: string) => Promise<DetectionResult>;
}

/**
 * Built-in: test connection via the REST API (internal mailbox).
 * This checks if the address belongs to a local mailbox with valid credentials.
 */
const restmailApiTest: DetectionTest = {
  id: 'restmail-api',
  label: 'REST Mail internal mailbox',
  run: async (address, password) => {
    try {
      const resp = await api.testConnection({ address, password });
      return {
        success: true,
        display_name: resp.data.display_name,
        method: 'restmail-api',
      };
    } catch (err) {
      return {
        success: false,
        method: 'restmail-api',
        error: err instanceof Error ? err.message : 'Connection failed',
      };
    }
  },
};

/**
 * The ordered list of detection tests. Tests run top-to-bottom.
 * The first successful test determines the account configuration.
 *
 * To add a new detection method (e.g., IMAP autoconfig, Mozilla ISPDB,
 * SRV record lookup), define a DetectionTest and push it here.
 */
export const detectionTests: DetectionTest[] = [
  restmailApiTest,
  // Future: imapAutoconfigTest, mozillaIspdbTest, srvRecordTest, etc.
];

export interface DetectionProgress {
  currentTest: string;
  completedTests: string[];
  result: DetectionResult | null;
}

/**
 * Runs all detection tests in order, returning the first successful result
 * or the last failure if none succeed.
 *
 * @param address - email address to test
 * @param password - password to test
 * @param onProgress - optional callback fired as each test starts/completes
 */
export async function runDetection(
  address: string,
  password: string,
  onProgress?: (progress: DetectionProgress) => void,
): Promise<DetectionResult> {
  const completed: string[] = [];
  let lastResult: DetectionResult | null = null;

  for (const test of detectionTests) {
    onProgress?.({
      currentTest: test.label,
      completedTests: [...completed],
      result: null,
    });

    const result = await test.run(address, password);
    completed.push(test.id);
    lastResult = result;

    if (result.success) {
      onProgress?.({
        currentTest: test.label,
        completedTests: [...completed],
        result,
      });
      return result;
    }
  }

  // All tests failed — return the last failure
  const fallback: DetectionResult = lastResult ?? {
    success: false,
    method: 'none',
    error: 'No detection tests configured',
  };

  onProgress?.({
    currentTest: '',
    completedTests: [...completed],
    result: fallback,
  });

  return fallback;
}
