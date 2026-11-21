import { vi, afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';
Object.defineProperty(window, 'matchMedia', { value: vi.fn(() => ({ matches: false, addListener() {}, removeListener() {} })) });
afterEach(cleanup);
