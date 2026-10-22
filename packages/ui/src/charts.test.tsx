import { fireEvent, render, screen, within } from '@testing-library/react';
import { LineChart, SERIES_MAX } from './components/charts';

// The metrics chart (L2.5-002 §14): series coloured by order, a legend with the newest values in text,
// a hidden table with the same numbers, a crosshair tooltip on hover, and an empty state.

const fmt = (v: number) => `${v} u`;

describe('LineChart', () => {
  const series = [
    { label: 'app/api', points: [{ t: 0, v: 1 }, { t: 30, v: 3 }] },
    { label: 'app/web', points: [{ t: 0, v: 2 }, { t: 30, v: 4 }] },
  ];

  it('draws one line per series in slot order, with a legend and a table', () => {
    const { container } = render(<LineChart title="CPU" series={series} from={0} to={30} format={fmt} />);
    const lines = container.querySelectorAll('polyline');
    expect([...lines].map((l) => l.getAttribute('class'))).toEqual(['es-chart-line es-series-1', 'es-chart-line es-series-2']);
    expect(screen.getByRole('figure', { name: 'CPU' })).toBeInTheDocument();
    const legend = screen.getByRole('list');
    expect(within(legend).getByText('app/api').nextSibling?.textContent).toBe('3 u');
    const table = screen.getByRole('table', { name: 'CPU' });
    expect(within(table).getAllByRole('row')).toHaveLength(3);
  });

  it('shows the time and every value under the pointer', () => {
    const { container } = render(<LineChart title="CPU" series={series} from={0} to={30} format={fmt} />);
    const svg = container.querySelector('svg.es-chart-svg') as SVGSVGElement;
    svg.getBoundingClientRect = () => ({ left: 0, width: 600, top: 0, height: 200, right: 600, bottom: 200, x: 0, y: 0, toJSON: () => ({}) });
    fireEvent.pointerMove(svg, { clientX: 590 });
    expect(container.querySelector('.es-chart-cross')).not.toBeNull();
    expect(container.querySelectorAll('.es-chart-tip-text')).toHaveLength(2);
    fireEvent.pointerLeave(svg);
    expect(container.querySelector('.es-chart-cross')).toBeNull();
  });

  it('never invents a ninth colour: extra series are named, not drawn', () => {
    const many = Array.from({ length: SERIES_MAX + 2 }, (_, i) => ({ label: `s${i}`, points: [{ t: 0, v: i }] }));
    const { container } = render(<LineChart title="Many" series={many} from={0} to={30} format={fmt} />);
    expect(container.querySelectorAll('polyline')).toHaveLength(SERIES_MAX);
    expect(screen.getByText('2 more not drawn')).toBeInTheDocument();
  });

  it('says so when there is no data', () => {
    render(<LineChart title="Memory" series={[{ label: 'a', points: [] }]} from={0} to={30} format={fmt} empty="No data yet." />);
    expect(screen.getByRole('img', { name: 'Memory: No data yet.' })).toBeInTheDocument();
  });
});
