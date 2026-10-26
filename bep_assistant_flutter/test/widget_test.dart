import 'package:flutter_test/flutter_test.dart';
import 'package:bep_assistant_flutter/main.dart';

void main() {
  testWidgets('App builds without crashing', (WidgetTester tester) async {
    await tester.pumpWidget(const MyApp(goReady: false, errorMsg: 'test'));
    expect(find.text('核心库加载失败'), findsOneWidget);
  });
}
