# coding rules

## coding rules
1. each function / method should not be encoded with more than 60 lines.
2. do not use literals (strings, numbers, bools, etc.) directly in normal codes. define them in constants, and refer to them with constants.
3. do not duplicate codes. if some code duplicated for more than twice, you should consider make a new abstraction for the duplicated code's responsibility.
4. only make unit test for the outside-aware units (those units with clear responsibility and can be used by other components. so even a refactoring happens within the unit, the behavior tested by the unit test still stay stable.), do not make unit test on inner-unit parts.

## Performance
1. benchmark for codes on the kernel path. (the whole path from notification to delivery to the vendor).

## Following are the anti-patterns. TAKE CARE.
1. compile regex each time it's used. (hurt performance)
