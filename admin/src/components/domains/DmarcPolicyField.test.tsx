import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { DmarcPolicyField } from "./DmarcPolicyField";

afterEach(cleanup);

it("offers the API's policies, says how to move through them and that DNS must be published", () => {
  const onChange = vi.fn();
  render(<DmarcPolicyField value="none" onChange={onChange} />);

  const select = screen.getByLabelText("DMARC policy") as HTMLSelectElement;
  expect([...select.options].map((o) => o.value)).toEqual(["none", "quarantine", "reject"]);
  expect(select.value).toBe("none");
  expect(screen.getByText(/Start at none and read the DMARC reports/)).toBeTruthy();
  expect(screen.getByText("chore dns:show")).toBeTruthy();
  expect(screen.getByText(/Publish the new record at your DNS provider/)).toBeTruthy();

  fireEvent.change(select, { target: { value: "quarantine" } });
  expect(onChange).toHaveBeenCalledWith("quarantine");
});
